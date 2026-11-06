package outside

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/recap"
	"github.com/Nadif243/fancord-bot/internal/relay"
	"github.com/Nadif243/fancord-bot/internal/youtube"
)

// Review actions, as they appear in the buttons.
const (
	ActionConfirm       = "confirm"
	ActionIgnore        = "ignore"
	ActionIgnoreChannel = "ignorechannel"
)

// Why an item was ignored.
const (
	ReasonNotFound       = "not found"           // private, deleted or a wrong link
	ReasonOwnChannel     = "her own channel"     // her own streams are handled elsewhere
	ReasonChannelIgnored = "channel ignored"     // a reviewer, or the config, said so
	ReasonReviewer       = "ignored by reviewer" // one video
	ReasonChannelButton  = "channel ignored by reviewer"
)

const (
	retryAfter    = 5 * time.Minute // after a failed YouTube lookup
	tickEvery     = 30 * time.Second
	seedWindow    = 14 * 24 * time.Hour
	videosPerCall = 50

	// Confirmed collabs are looked at again until they end. Close to the start (or while live) that happens every few minutes; far from it, rarely, to save YouTube quota (one call, whatever the number of videos, is 1 unit).
	recheckSoon    = 2 * time.Minute
	recheckLater   = 15 * time.Minute
	soonWindow     = 3 * time.Hour
	maxAhead       = 14 * 24 * time.Hour // a waiting room further away is not announced yet
	missesToGiveUp = 3
)

// Videos is the part of YouTube this package needs. *youtube.Client has it.
type Videos interface {
	Videos(ctx context.Context, ids []string) (map[string]youtube.Video, error)
}

// Button is one button under a review message.
type Button struct {
	Label    string
	CustomID string
	Danger   bool
}

// Poster puts the review message in the review channel.
type Poster interface {
	Post(channelID, content string, buttons []Button) (messageID string, err error)
}

// Matcher finds partners.csv names in a text. *partners.Book has it.
type Matcher interface {
	Match(text string) []string
}

// Announcer posts and edits the announcement of one collab.
// *broadcast.Announcer satisfies it.
type Announcer interface {
	Update(s broadcast.Stream) error
}

// Settings are the choices from the config.
type Settings struct {
	ChannelID      string   // where the review messages go
	OwnChannelID   string   // her channel: its videos are not "other channels"
	MentionWords   []string // words that show a video is about her
	IgnoreChannels []string // channel IDs that are never shown
}

type queued struct{ source, ref string }

// Service looks candidates up and posts them for review.
type Service struct {
	file  *File
	vids  Videos
	post  Poster
	match Matcher
	set   Settings
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	queue   map[string]queued // video ID -> where it came from
	order   []string          // queue order
	nextTry time.Time         // after a failed lookup: no lookups before this
	wake    chan struct{}

	announcer Announcer // nil: confirmed collabs are not announced
	nextCheck time.Time // no recheck of confirmed collabs before this
}

// WithAnnouncer turns on announcing confirmed collabs.
func (s *Service) WithAnnouncer(a Announcer) *Service {
	s.announcer = a
	return s
}

func New(f *File, v Videos, p Poster, m Matcher, set Settings, log *slog.Logger) *Service {
	for i := range set.MentionWords {
		set.MentionWords[i] = strings.ToLower(strings.TrimSpace(set.MentionWords[i]))
	}
	set.MentionWords = slices.DeleteFunc(slices.Clone(set.MentionWords), func(w string) bool { return w == "" })
	return &Service{file: f, vids: v, post: p, match: m, set: set, log: log, now: time.Now,
		queue: map[string]queued{}, wake: make(chan struct{}, 1)}
}

// Enqueue puts videos in line for a lookup and returns how many were new:
// a video that was already looked at, or is already in line, is skipped.
func (s *Service) Enqueue(source, ref string, ids ...string) int {
	s.mu.Lock()
	n := 0
	for _, id := range ids {
		if _, seen := s.file.Get(id); seen {
			continue
		}
		if _, waiting := s.queue[id]; waiting {
			continue
		}
		s.queue[id] = queued{source, ref}
		s.order = append(s.order, id)
		n++
	}
	s.mu.Unlock()
	if n > 0 {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return n
}

// Seed re-queues the YouTube links from her recent posts, so a restart (or the first start of this feature) does not miss links the relay already saw.
// Videos that were looked at before are skipped by Enqueue.
func (s *Service) Seed(links []relay.Link) {
	cutoff := s.now().Add(-seedWindow)
	for _, l := range links {
		if l.Found.Before(cutoff) {
			continue
		}
		s.Enqueue("tweet", l.PostID, l.VideoID)
	}
}

// Run works until ctx ends: it looks up what is in line and posts what is waiting for a review message.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("collab review is on", "channel", s.set.ChannelID)
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		s.Work(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Work does one round: lookups first, then review messages not yet posted, then the confirmed collabs.
func (s *Service) Work(ctx context.Context) {
	s.lookup(ctx)
	s.postWaiting()
	s.recheck(ctx)
}

// fetch looks the videos up, 50 at a time. On a failure it starts the wait before the next try and returns the error.
func (s *Service) fetch(ctx context.Context, ids []string) (map[string]youtube.Video, error) {
	all := make(map[string]youtube.Video, len(ids))
	for len(ids) > 0 {
		batch := ids[:min(len(ids), videosPerCall)]
		ids = ids[len(batch):]
		found, err := s.vids.Videos(ctx, batch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("collab review: YouTube lookup failed, will try again", "err", err, "in", retryAfter)
			}
			s.mu.Lock()
			s.nextTry = s.now().Add(retryAfter)
			s.mu.Unlock()
			return nil, err
		}
		for id, v := range found {
			all[id] = v
		}
	}
	return all, nil
}

func (s *Service) lookup(ctx context.Context) {
	s.mu.Lock()
	if len(s.order) == 0 || s.now().Before(s.nextTry) {
		s.mu.Unlock()
		return
	}
	ids := slices.Clone(s.order)
	from := make(map[string]queued, len(ids))
	for _, id := range ids {
		from[id] = s.queue[id]
	}
	s.mu.Unlock()

	found, err := s.fetch(ctx, ids)
	if err != nil {
		return
	}
	for _, id := range ids {
		it := s.judge(id, from[id], found)
		if err := s.file.Put(it); err != nil {
			s.log.Warn("collab review: could not save", "video", id, "err", err)
		}
		s.dequeue(id)
	}
}

func (s *Service) dequeue(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queue, id)
	s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
}

// judge turns a lookup result into an item, ignoring it at once when a rule says so (and remembering that, so the video is never looked up again).
func (s *Service) judge(id string, from queued, found map[string]youtube.Video) Item {
	it := Item{VideoID: id, Source: from.source, SourceRef: from.ref, Status: Pending, Added: s.now().UTC()}
	v, ok := found[id]
	if !ok {
		return s.ignore(it, ReasonNotFound)
	}
	it.Title, it.ChannelID, it.ChannelTitle = v.Title, v.ChannelID, v.ChannelTitle
	it.State, it.Kind, it.Start, it.End = v.State, v.Kind, v.Start, v.End
	hay := strings.ToLower(v.Title + "\n" + v.Description)
	it.Mentions = slices.ContainsFunc(s.set.MentionWords, func(w string) bool { return strings.Contains(hay, w) })
	it.Partners = s.partners(v)

	switch {
	case s.set.OwnChannelID != "" && v.ChannelID == s.set.OwnChannelID:
		return s.ignore(it, ReasonOwnChannel)
	case slices.Contains(s.set.IgnoreChannels, v.ChannelID) || s.file.ChannelIgnored(v.ChannelID):
		return s.ignore(it, ReasonChannelIgnored)
	}
	return it
}

func (s *Service) ignore(it Item, reason string) Item {
	it.Status, it.Reason, it.Decided, it.DecidedBy = Ignored, reason, s.now().UTC(), "rule"
	return it
}

// partners lists the partners.csv names found in the channel name or title.
func (s *Service) partners(v youtube.Video) []string {
	if s.match == nil {
		return nil
	}
	var out []string
	for _, name := range append(s.match.Match(v.ChannelTitle), s.match.Match(v.Title)...) {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// postWaiting posts the review message of every pending item that has none (new ones, and ones whose post failed before).
func (s *Service) postWaiting() {
	for _, it := range s.file.Items() {
		if it.Status != Pending || it.MessageID != "" {
			continue
		}
		id, err := s.post.Post(s.set.ChannelID, Text(it), Buttons(it.VideoID, it.ChannelID != ""))
		if err != nil {
			s.log.Warn("collab review: could not post the review message, will try again", "video", it.VideoID, "err", err)
			return
		}
		if _, err := s.file.Update(it.VideoID, func(x *Item) bool { x.MessageID = id; return true }); err != nil {
			s.log.Warn("collab review: could not save", "video", it.VideoID, "err", err)
		}
	}
}

// Result is the outcome of a button press.
type Result struct {
	Text    string // the review message as it should read now
	Changed bool   // false when somebody had already decided
}

// Review applies a decision. Only the first decision on a video counts: a second press (or a second reviewer) gets the text of the first one back.
func (s *Service) Review(action, videoID, by string) (Result, error) {
	var want Status
	reason := ""
	switch action {
	case ActionConfirm:
		want = Confirmed
	case ActionIgnore:
		want, reason = Ignored, ReasonReviewer
	case ActionIgnoreChannel:
		want, reason = Ignored, ReasonChannelButton
	default:
		return Result{}, ErrUnknown
	}
	changed := false
	it, err := s.file.Update(videoID, func(x *Item) bool {
		if x.Status != Pending {
			return false
		}
		x.Status, x.Reason, x.Decided, x.DecidedBy = want, reason, s.now().UTC(), by
		changed = true
		return true
	})
	if err != nil && it.VideoID == "" {
		return Result{}, err // unknown video
	}
	if changed && action == ActionIgnoreChannel && it.ChannelID != "" {
		if cerr := s.file.IgnoreChannel(it.ChannelID, it.ChannelTitle); cerr != nil && err == nil {
			err = cerr
		}
	}
	if changed && want == Confirmed {
		s.kick()
	}
	return Result{Text: DecidedText(it), Changed: changed}, err
}

// kick makes the confirmed collabs be looked at right away.
func (s *Service) kick() {
	s.mu.Lock()
	s.nextCheck = time.Time{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Text is the review message for a pending item.
func Text(it Item) string { return msg.CollabCandidate(view(it)) }

// DecidedText is the review message once somebody decided.
func DecidedText(it Item) string {
	decision := "ignored"
	switch {
	case it.Status == Confirmed:
		decision = "confirmed"
	case it.Reason == ReasonChannelButton:
		decision = "channel-ignored"
	}
	return msg.CollabDecided(view(it), decision, it.DecidedBy)
}

// Buttons are the buttons under a pending review message. The custom ID is "collab:<action>:<videoID>" (see ParseCustomID).
func Buttons(videoID string, canIgnoreChannel bool) []Button {
	b := []Button{
		{Label: "Confirm collab", CustomID: customID(ActionConfirm, videoID)},
		{Label: "Ignore", CustomID: customID(ActionIgnore, videoID), Danger: true},
	}
	if canIgnoreChannel {
		b = append(b, Button{Label: "Ignore this channel", CustomID: customID(ActionIgnoreChannel, videoID), Danger: true})
	}
	return b
}

func customID(action, videoID string) string { return "collab:" + action + ":" + videoID }

// ParseCustomID undoes Buttons: ok is false for any other component.
func ParseCustomID(id string) (action, videoID string, ok bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "collab" || parts[2] == "" {
		return "", "", false
	}
	switch parts[1] {
	case ActionConfirm, ActionIgnore, ActionIgnoreChannel:
		return parts[1], parts[2], true
	}
	return "", "", false
}

func view(it Item) msg.CollabCandidateView {
	state := ""
	switch it.State {
	case broadcast.Upcoming:
		state = "upcoming"
	case broadcast.Live:
		state = "live"
	case broadcast.Ended:
		state = "ended"
	}
	return msg.CollabCandidateView{
		Title: it.Title, ChannelTitle: it.ChannelTitle, URL: "https://www.youtube.com/watch?v=" + it.VideoID,
		State: state, Premiere: it.Kind == broadcast.KindPremiere, Start: it.Start,
		Mentions: it.Mentions, Partners: it.Partners, Source: it.Source, SourceRef: it.SourceRef,
	}
}

var linkText = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.|m\.|music\.)?(?:youtube\.com|youtu\.be)/[^\s<>()\[\]]+`)

// YouTubeIDs finds the YouTube videos linked in a piece of text, once each, in the order they appear.
func YouTubeIDs(text string) []string {
	var out []string
	for _, raw := range linkText.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:!?'\"")
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		if id, ok := relay.YouTubeID(raw); ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// active lists the confirmed collabs that are still being followed.
func (s *Service) active() []Item {
	var out []Item
	for _, it := range s.file.Items() {
		if it.Status == Confirmed && !it.Done {
			out = append(out, it)
		}
	}
	return out
}

// recheck looks at every confirmed collab again and announces it, or updates its announcement, once every check passes:
//   - YouTube still shows the video (it was not deleted or made private),
//   - its channel was not ignored in the meantime,
//   - it is a real stream (not an ordinary video or a premiere),
//   - a waiting room has a start time, and is no more than 14 days away.
//
// Anything else is simply looked at again later (or, when it can never qualify, given up on).
func (s *Service) recheck(ctx context.Context) {
	if s.announcer == nil {
		return
	}
	s.mu.Lock()
	wait := s.now().Before(s.nextCheck) || s.now().Before(s.nextTry)
	s.mu.Unlock()
	items := s.active()
	if wait || len(items) == 0 {
		return
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.VideoID
	}
	found, err := s.fetch(ctx, ids)
	if err != nil {
		return
	}
	for _, it := range items {
		s.settle(it.VideoID, found)
	}
	next := recheckLater
	for _, it := range s.active() {
		if soon(it, s.now()) {
			next = recheckSoon
		}
	}
	s.mu.Lock()
	s.nextCheck = s.now().Add(next)
	s.mu.Unlock()
}

// soon is true when a collab is live, about to start, has no known start, or is over but not finished with (its last announcement failed).
func soon(it Item, now time.Time) bool {
	return it.State == broadcast.Live || it.State == broadcast.Ended || it.Start.IsZero() || it.Start.Sub(now) < soonWindow
}

func (s *Service) settle(id string, found map[string]youtube.Video) {
	var stream broadcast.Stream
	announce := false
	v, ok := found[id]
	// Asked before the update: the file's lock is held inside it.
	ignored := ok && (slices.Contains(s.set.IgnoreChannels, v.ChannelID) || s.file.ChannelIgnored(v.ChannelID))
	it, err := s.file.Update(id, func(x *Item) bool {
		switch {
		case !ok:
			x.Missing++
			if x.Missing >= missesToGiveUp {
				x.Done = true
				s.log.Warn("collab review: a confirmed video is gone (deleted or private); no longer followed", "video", id)
			}
			return true
		case s.set.OwnChannelID != "" && v.ChannelID == s.set.OwnChannelID, ignored:
			x.Done = true
			return true
		}
		x.Missing = 0
		x.Title, x.ChannelID, x.ChannelTitle = v.Title, v.ChannelID, v.ChannelTitle
		x.State, x.Kind, x.Start, x.End = v.State, v.Kind, v.Start, v.End
		switch {
		case v.State == 0 || v.Kind == broadcast.KindPremiere:
			x.Done = true // an ordinary video or a premiere is not a stream announcement
		case v.State == broadcast.Upcoming && (v.Start.IsZero() || v.Start.Sub(s.now()) > maxAhead):
			// no start time yet, or placeholder far ahead: not announced, looked at again
		default:
			announce = true
			stream = broadcast.Stream{VideoID: id, Title: v.Title, State: v.State, Kind: v.Kind,
				StartAt: v.Start, EndAt: v.End, Channel: v.ChannelTitle}
			if v.State == broadcast.Ended {
				x.Done = true
			}
		}
		return true
	})
	if err != nil {
		s.log.Warn("collab review: could not save", "video", id, "err", err)
	}
	if !announce {
		return
	}
	if err := s.announcer.Update(stream); err != nil {
		s.log.Warn("collab review: announcement failed, will try again", "video", id, "err", err)
		if it.Done {
			// Not announced and not marked: try again at the next recheck.
			_, _ = s.file.Update(id, func(x *Item) bool { x.Done = false; return true })
		}
	}
}

// Guests lists the confirmed stream collabs that ended in [from, to), for the weekly recap. A collab counts on the day it ended, like her own streams.
func (s *Service) Guests(from, to time.Time) []recap.Guest {
	var out []recap.Guest
	for _, it := range s.file.Items() {
		if it.Status != Confirmed || it.State != broadcast.Ended || it.Kind == broadcast.KindPremiere {
			continue
		}
		at := it.End
		if at.IsZero() {
			at = it.Start
		}
		if at.IsZero() || at.Before(from) || !at.Before(to) {
			continue
		}
		g := recap.Guest{Title: it.Title, Channel: it.ChannelTitle, URL: "https://www.youtube.com/watch?v=" + it.VideoID,
			Start: it.Start, Partners: slices.Clone(it.Partners)}
		if g.Start.IsZero() {
			g.Start = at
		}
		if !it.Start.IsZero() && it.End.After(it.Start) {
			g.Duration = it.End.Sub(it.Start)
		}
		out = append(out, g)
	}
	return out
}
