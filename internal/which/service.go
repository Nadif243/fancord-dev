// Package which asks "who is meant?" when a stream title holds a nickname that
// two or more partners share (the partner list allows that; see package
// partners). The question goes to the review channel with one button per
// partner. The answer is written down as a by-hand tag, so the recap and
// /collab see it like any other tag, and the same question is never asked twice.
package which

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/outside"
	"github.com/Nadif243/fancord-bot/internal/partners"
)

const (
	defaultEvery  = 2 * time.Minute
	defaultWindow = 8 * 24 * time.Hour // a bit more than the week the recap covers
	pool          = 60                 // how many recent streams are looked at
	maxPerScan    = 5                  // a first run after an update must not flood the channel
	maxCandidates = 4                  // a row holds five buttons, and "Neither" is one of them
)

// ErrBadChoice means the button does not belong to this question.
var ErrBadChoice = errors.New("not one of the choices")

// Streams is where recent streams come from. *history.File satisfies it.
type Streams interface {
	Recent(n int) []history.Entry
}

// Asker finds the shared nicknames in a title. *collabs.Service satisfies it.
type Asker interface {
	Ambiguities(title string) []partners.Ambiguity
}

// Tags is the by-hand tag store. *collabs.File satisfies it.
type Tags interface {
	Get(videoID string) (added, removed []string)
	Add(videoID string, names []string) error
	Checked(videoID string) bool
}

// Poster puts a message with buttons in a channel. *discord.SessionMessenger satisfies it (the review messages use the same call).
type Poster interface {
	Post(channelID, content string, buttons []outside.Button) (messageID string, err error)
}

// Settings are the choices from the config.
type Settings struct {
	ChannelFn func() string // the review channel, asked each time (/setup can change it)
	Every     time.Duration // how often to look; 0 means two minutes
	Window    time.Duration // how far back; 0 means eight days
}

// Service asks the questions and takes the answers.
type Service struct {
	file    *File
	streams Streams
	asker   Asker
	tags    Tags
	post    Poster
	set     Settings
	log     *slog.Logger
	now     func() time.Time
}

func New(f *File, streams Streams, asker Asker, tags Tags, post Poster, set Settings, log *slog.Logger) *Service {
	if set.Every <= 0 {
		set.Every = defaultEvery
	}
	if set.Window <= 0 {
		set.Window = defaultWindow
	}
	return &Service{file: f, streams: streams, asker: asker, tags: tags, post: post, set: set, log: log, now: time.Now}
}

// Run looks for questions to ask until ctx ends.
func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second) // let the poller fill the history first
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.Scan()
			timer.Reset(s.set.Every)
		}
	}
}

// Scan asks every question that is due: a recent stream whose title holds a shared nickname that nothing settles yet, that nobody has tagged by hand and that was not asked about before.
func (s *Service) Scan() {
	channel := ""
	if s.set.ChannelFn != nil {
		channel = s.set.ChannelFn()
	}
	if channel == "" {
		return
	}
	asked := 0
	for _, e := range s.streams.Recent(pool) {
		at := e.Start
		if at.IsZero() {
			at = e.End
		}
		if at.IsZero() || s.now().Sub(at) > s.set.Window {
			continue
		}
		if s.tags.Checked(e.VideoID) {
			continue // a person already went through this stream
		}
		added, removed := s.tags.Get(e.VideoID)
		for _, a := range s.asker.Ambiguities(e.Title) {
			if touches(a.Candidates, added) || touches(a.Candidates, removed) {
				continue // somebody already tagged one of them by hand
			}
			if _, done := s.file.Find(e.VideoID, a.Word); done {
				continue
			}
			if len(a.Candidates) > maxCandidates {
				s.log.Warn("nickname shared by too many partners to ask about", "word", a.Word, "partners", len(a.Candidates))
				continue
			}
			if asked == maxPerScan {
				return
			}
			it := Item{VideoID: e.VideoID, Title: e.Title, Start: at, Word: a.Word, Candidates: a.Candidates, Status: Open, Asked: s.now().UTC()}
			if !s.ask(channel, it) {
				return // the channel is unreachable; try again next time
			}
			asked++
		}
	}
}

// ask saves the question and posts it. It reports false when posting failed.
func (s *Service) ask(channel string, it Item) bool {
	// Saved first, so a crash between the two steps cannot ask twice.
	saved, err := s.file.Add(it)
	if err != nil {
		s.log.Warn("nickname question: could not save (it holds until restart)", "err", err)
	}
	id, err := s.post.Post(channel, msg.WhichQuestion(view(saved)), Buttons(saved))
	if err != nil {
		s.log.Warn("nickname question: could not post, will try again", "video", it.VideoID, "word", it.Word, "err", err)
		s.file.drop(saved.Key) // taken back, so the next scan asks again
		return false
	}
	if _, err := s.file.Update(saved.Key, func(x *Item) bool { x.MessageID = id; return true }); err != nil {
		s.log.Warn("nickname question: could not save the message ID", "err", err)
	}
	return true
}

// Result is the outcome of a button press.
type Result struct {
	Text    string // the question as it should read now
	Changed bool   // false when somebody had already answered
}

// Answer applies a button press: choice is a candidate's number, "all" or "none". Only the first answer counts; a second press gets the text of the first one back. The partners chosen are tagged on the stream.
func (s *Service) Answer(key, choice, by string) (Result, error) {
	changed := false
	var chosen []string
	it, err := s.file.Update(key, func(x *Item) bool {
		if x.Status != Open {
			return false
		}
		names, ok := pick(x.Candidates, choice)
		if !ok {
			return false
		}
		x.Status, x.Chosen, x.DecidedBy, x.Decided = Done, names, by, s.now().UTC()
		chosen, changed = names, true
		return true
	})
	if errors.Is(err, ErrUnknown) {
		return Result{}, err
	}
	if it.Status != Done {
		return Result{}, ErrBadChoice
	}
	if changed && len(chosen) > 0 {
		if terr := s.tags.Add(it.VideoID, chosen); terr != nil {
			err = errors.Join(err, terr)
		}
	}
	return Result{Text: msg.WhichDecided(view(it), it.Chosen, it.DecidedBy), Changed: changed}, err
}

// pick turns a button's choice into partners. ok is false for a choice that does not fit the candidates.
func pick(candidates []string, choice string) (names []string, ok bool) {
	switch choice {
	case "none":
		return nil, true
	case "all":
		return append([]string(nil), candidates...), true
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 0 || n >= len(candidates) {
		return nil, false
	}
	return []string{candidates[n]}, true
}

func view(it Item) msg.WhichView {
	return msg.WhichView{Word: it.Word, Title: it.Title, VideoID: it.VideoID, Start: it.Start, Candidates: it.Candidates}
}

// touches reports whether the two lists share a name.
func touches(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// Buttons are the buttons under a question: one per partner, "Both" when there are two or three, and "Neither". The custom ID is "which:<key>:<choice>".
func Buttons(it Item) []outside.Button {
	var out []outside.Button
	for i, name := range it.Candidates {
		out = append(out, outside.Button{Label: name, CustomID: customID(it.Key, strconv.Itoa(i))})
	}
	if n := len(it.Candidates); n >= 2 && n <= 3 {
		out = append(out, outside.Button{Label: "Both", CustomID: customID(it.Key, "all")})
	}
	return append(out, outside.Button{Label: "Neither", CustomID: customID(it.Key, "none"), Danger: true})
}

func customID(key, choice string) string { return "which:" + key + ":" + choice }

// ParseCustomID undoes Buttons: ok is false for any other component.
func ParseCustomID(id string) (key, choice string, ok bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "which" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}
