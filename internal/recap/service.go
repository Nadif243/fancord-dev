package recap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Source is the stream history. *history.File satisfies it.
type Source interface {
	Ended(from, to time.Time) []history.Entry
}

// Sender posts a message. The Discord messenger satisfies it, and it already refuses to ping anybody.
type Sender interface {
	Send(channelID, content string) (messageID string, err error)
}

// Marker remembers which recap was handled last, so a restart does not post the same one twice. *store.File satisfies it.
type Marker interface {
	LastRecap() time.Time
	SetLastRecap(time.Time) error
}

const (
	checkEvery = time.Minute
	// A recap that is more than this late (the bot was off all Sunday night and the next day) is skipped, not posted as stale news.
	catchUp = 24 * time.Hour
)

// Service posts the recap every Sunday evening.
type Service struct {
	src       Source
	send      Sender
	marker    Marker
	channelID string
	settle    time.Duration // how long after the due time to wait before posting
	log       *slog.Logger
	now       func() time.Time
}

// New makes a Service. settle should be a little longer than the poll interval: the stream that ended just before the deadline has to be seen as ended by the poller before the recap counts it.
func New(src Source, send Sender, marker Marker, channelID string, settle time.Duration, log *slog.Logger) *Service {
	return &Service{src: src, send: send, marker: marker, channelID: channelID, settle: settle, log: log, now: time.Now}
}

// Run checks once a minute whether a recap is due, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("weekly recap is on", "channel", s.channelID, "next", LastDue(s.now()).AddDate(0, 0, 7).Add(s.settle).Format(time.RFC1123))
	s.tick()
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Service) tick() {
	now := s.now()
	due := LastDue(now)
	done := s.marker.LastRecap()

	if !done.Before(due) {
		return // this week's recap is already handled
	}
	if done.IsZero() {
		// Never posted before: this is the first start. The history may only cover part of the week, so wait for a full one.
		s.log.Info("first start: the first recap will be posted at the next due time", "skipping", due)
		s.remember(due)
		return
	}
	if now.Before(due.Add(s.settle)) {
		return
	}
	if now.Sub(due) > catchUp {
		s.log.Warn("the bot was off when the recap was due; skipping it", "due", due)
		s.remember(due)
		return
	}

	texts := s.render(due)
	if err := s.post(s.channelID, texts, func() { s.remember(due) }); err != nil {
		s.log.Error("could not post the weekly recap; will try again", "err", err)
	}
}

// render builds the messages for the window ending at due.
func (s *Service) render(due time.Time) []string {
	from := due.Add(-window)
	return msg.Recap(Build(s.src.Ended(from, due), from, due))
}

// post sends the messages in order. afterFirst runs once the first message is out: from then on the recap counts as posted, so a failure further down leaves a short recap rather than causing a second full one.
func (s *Service) post(channelID string, texts []string, afterFirst func()) error {
	for n, text := range texts {
		if _, err := s.send.Send(channelID, text); err != nil {
			return fmt.Errorf("message %d of %d: %w", n+1, len(texts), err)
		}
		if n == 0 && afterFirst != nil {
			afterFirst()
		}
	}
	return nil
}

func (s *Service) remember(due time.Time) {
	if err := s.marker.SetLastRecap(due); err != nil {
		// Kept in memory for this run; only a restart could repeat the recap.
		s.log.Error("could not save which recap was posted", "err", err)
	}
}

// Preview posts the recap of a past week to channelID without touching the "already posted" memory. weeksAgo 0 is the most recent complete week.
func (s *Service) Preview(channelID string, weeksAgo int) error {
	if weeksAgo < 0 {
		return errors.New("weeksAgo cannot be negative")
	}
	due := LastDue(s.now()).AddDate(0, 0, -7*weeksAgo)
	return s.post(channelID, s.render(due), nil)
}
