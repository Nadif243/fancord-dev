package recap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Source is the stream history. *history.File satisfies it.
type Source interface {
	// Week is what a recap for [from, to) counts; carry is explained there.
	Week(from, to, carry time.Time) []history.Entry
	// Running is the streams that are live now and started in [since, before).
	Running(since, before time.Time) []history.Entry
}

// Sender posts a message. The Discord messenger satisfies it, and it already refuses to ping anybody.
type Sender interface {
	Send(channelID, content string) (messageID string, err error)
}

// Marker remembers which recap was handled last, so a restart does not post the same one twice. *store.File satisfies it.
type Marker interface {
	LastRecap() time.Time
	SetLastRecap(time.Time) error
	// LastRecapPosted is when that recap actually went out (zero if unknown).
	LastRecapPosted() time.Time
	SetLastRecapPosted(time.Time) error
}

// ReminderMarker does the same for the Saturday reminder. *store.File satisfies it.
type ReminderMarker interface {
	LastReminder() time.Time
	SetLastReminder(time.Time) error
}

const (
	checkEvery = time.Minute
	// A recap that is more than this late (the bot was off all Sunday night and the next day) is skipped, not posted as stale news.
	catchUp = 24 * time.Hour

	// When a stream is still live at the due time, the recap waits for it to end, but never longer than this. After that it goes out without it, and the stream is counted in the next recap.
	holdMax = 3 * time.Hour

	// Only a stream that started within this long before the due time can hold the recap back: an older "live" entry is a leftover, not a stream.
	maxRun = 24 * time.Hour
)

// Service posts the recap every Sunday evening.
// Every channel is a function that is asked each time something is posted, so /setup can change it while the bot runs.
type Service struct {
	src     Source
	send    Sender
	marker  Marker
	channel func() string // where the recap goes
	settle  time.Duration // how long after the due time to wait before posting
	log     *slog.Logger
	now     func() time.Time

	partners Matcher       // nil: no collab line
	ops      func() string // where the "unknown handles" note goes; nil: no note

	remindTo func() string  // where the Saturday reminder goes; nil: no reminder
	remarker ReminderMarker // remembers the last reminder handled
	guests   Guests         // collabs on other channels; nil: the recap has none

	waiting bool // already said in the log that the recap waits for a live stream
}

// New makes a Service. settle should be a little longer than the poll interval: the stream that ended just before the deadline has to be seen as ended by the poller before the recap counts it.
func New(src Source, send Sender, marker Marker, channel func() string, settle time.Duration, log *slog.Logger) *Service {
	return &Service{src: src, send: send, marker: marker, channel: channel, settle: settle, log: log, now: time.Now}
}

// WithPartners turns on the collab line. A note about @handles that are not in the partner list is sent to the ops channel after each weekly recap (and to the channel of a /devrecap preview). ops may be nil.
func (s *Service) WithPartners(m Matcher, ops func() string) *Service {
	s.partners = m
	s.ops = ops
	return s
}

// WithGuests adds the week's confirmed collabs on other channels to the recap.
func (s *Service) WithGuests(g Guests) *Service {
	s.guests = g
	return s
}

// WithReminder turns on the Saturday reminder: streams of the week whose collabs nobody has checked are listed in the channel (the dev channel) at Saturday 12:00 WIB. It needs WithPartners, because "checked" lives there. The reminder only runs while the channel function gives a channel.
func (s *Service) WithReminder(channel func() string, m ReminderMarker) *Service {
	s.remindTo = channel
	s.remarker = m
	return s
}

// where is what a channel function says right now; a nil function says "".
func where(channel func() string) string {
	if channel == nil {
		return ""
	}
	return channel()
}

// Run checks once a minute whether a recap is due, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("weekly recap is on", "channel", s.channel(), "next", NextDue(s.now()).Add(s.settle).Format(time.RFC1123))
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
	s.remind()
	s.tickRecap()
}

func (s *Service) tickRecap() {
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
	if live := s.src.Running(due.Add(-maxRun), due); len(live) > 0 && now.Before(due.Add(holdMax)) {
		if !s.waiting {
			s.waiting = true
			s.log.Info("a stream is still live; the recap waits for it to end", "stream", live[0].VideoID, "at_most_until", due.Add(holdMax))
		}
		return
	}
	if now.Sub(due) > catchUp {
		s.log.Warn("the bot was off when the recap was due; skipping it", "due", due)
		s.remember(due)
		return
	}

	texts, note := s.render(due)
	if err := s.post(s.channel(), texts, func() { s.remember(due); s.rememberPosted(now) }); err != nil {
		s.log.Error("could not post the weekly recap; will try again", "err", err)
		return
	}
	s.waiting = false
	s.sendNote(where(s.ops), note)
}

// week is what a recap for [from, to) counts. A stream that started before the window and ended after the previous recap went out was left out of that one, so it is counted here. When the previous recap's posting time is not known, it is taken to be just after its due time.
func (s *Service) week(from, to time.Time) []history.Entry {
	carry := from.Add(s.settle)
	if s.marker.LastRecap().Equal(from) {
		if posted := s.marker.LastRecapPosted(); posted.After(carry) {
			carry = posted
		}
	}
	return s.src.Week(from, to, carry)
}

// render builds the messages for the window ending at due, and the ops note about unknown handles ("" when there is nothing to say).
func (s *Service) render(due time.Time) (texts []string, note string) {
	from := due.Add(-window)
	entries := s.week(from, due)
	var guests []Guest
	if s.guests != nil {
		guests = s.guests.Guests(from, due)
	}
	texts = msg.Recap(Build(entries, guests, from, due, s.partners))
	if unknown := UnknownHandles(entries, s.partners); len(unknown) > 0 {
		note = msg.RecapUnknownHandles(unknown)
	}
	return texts, note
}

// sendNote posts the ops note. It is a courtesy: a failure is only logged.
func (s *Service) sendNote(channelID, note string) {
	if note == "" || channelID == "" {
		return
	}
	if _, err := s.send.Send(channelID, note); err != nil {
		s.log.Warn("could not post the unknown-handles note", "err", err)
	}
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

func (s *Service) rememberPosted(at time.Time) {
	if err := s.marker.SetLastRecapPosted(at); err != nil {
		s.log.Error("could not save when the recap was posted", "err", err)
	}
}

// remind posts the Saturday reminder when it is due.
func (s *Service) remind() {
	channel := where(s.remindTo)
	if channel == "" || s.remarker == nil || s.partners == nil {
		return
	}
	now := s.now()
	due, recapDue := ReminderDue(now)
	if !s.remarker.LastReminder().Before(due) {
		return // this week's reminder is already handled
	}
	if now.Sub(due) > remindGrace {
		s.log.Warn("the bot was off when the reminder was due; skipping it", "due", due)
		s.rememberReminder(due)
		return
	}
	texts, pending, _ := s.reminder(recapDue)
	if pending == 0 {
		s.rememberReminder(due) // nothing to check: stay quiet
		return
	}
	if err := s.post(channel, texts, func() { s.rememberReminder(due) }); err != nil {
		s.log.Error("could not post the collab reminder; will try again", "err", err)
	}
}

// reminder builds the messages for the week that ends with the recap at recapDue: the streams so far whose collabs are not checked. pending is how many that is, total how many streams the week has so far.
func (s *Service) reminder(recapDue time.Time) (texts []string, pending, total int) {
	entries := s.week(recapDue.Add(-window), recapDue)
	var streams []msg.RecapStream
	for _, e := range entries {
		if s.partners.Checked(e.VideoID) {
			continue
		}
		st, _ := streamOf(e, s.partners)
		streams = append(streams, st)
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].Start.Before(streams[j].Start) })
	if len(streams) == 0 {
		return nil, 0, len(entries)
	}
	return msg.CollabReminder(recapDue.Add(-window), recapDue, len(entries), streams), len(streams), len(entries)
}

func (s *Service) rememberReminder(due time.Time) {
	if err := s.remarker.SetLastReminder(due); err != nil {
		s.log.Error("could not save which reminder was posted", "err", err)
	}
}

// PreviewReminder posts the reminder for the week in progress (the one the next recap will cover) to channelID right now, without touching the "already posted" memory. It says how many streams were listed and how many the week has so far; nothing is posted when none is listed.
func (s *Service) PreviewReminder(channelID string) (pending, total int, err error) {
	if s.partners == nil {
		return 0, 0, errors.New("the reminder needs the partner matching")
	}
	// Not ReminderDue: before Saturday noon that still points at the week that is over, but a preview should show the week people are checking now.
	texts, pending, total := s.reminder(NextDue(s.now()))
	if pending == 0 {
		return 0, total, nil
	}
	return pending, total, s.post(channelID, texts, nil)
}

// Preview posts a recap to channelID without touching the "already posted" memory. weeksAgo 0 is the week in progress (what the coming Sunday recap would say right now), 1 the most recent complete week, and so on.
func (s *Service) Preview(channelID string, weeksAgo int) error {
	if weeksAgo < 0 {
		return errors.New("weeksAgo cannot be negative")
	}
	due := NextDue(s.now()).AddDate(0, 0, -7*weeksAgo)
	texts, note := s.render(due)
	if err := s.post(channelID, texts, nil); err != nil {
		return err
	}
	s.sendNote(channelID, note)
	return nil
}
