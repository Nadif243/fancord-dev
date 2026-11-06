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

// Work does one round: lookups first, then any review messages not yet posted.
func (s *Service) Work(ctx context.Context) {
	s.lookup(ctx)
	s.postWaiting()
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
			return
		}
		for _, id := range batch {
			it := s.judge(id, from[id], found)
			if err := s.file.Put(it); err != nil {
				s.log.Warn("collab review: could not save", "video", id, "err", err)
			}
			s.dequeue(id)
		}
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
	return Result{Text: DecidedText(it), Changed: changed}, err
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
