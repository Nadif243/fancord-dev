// Package poller checks YouTube on a timer and hands the streams it finds to the announcer.
package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
)

const (
	// An upcoming stream scheduled further ahead than this is a placeholder (a permanent "free chat" room, say), not an announcement. It is picked up once its start time comes within range.
	maxAhead = 14 * 24 * time.Hour

	// A waiting room whose start time passed longer ago than this was left behind, not delayed. Announcing it would only confuse people.
	staleAfter = 12 * time.Hour

	// The longest one check may take, however slow YouTube is.
	checkTimeout = 30 * time.Second
)

// Source lists a channel's recent streams. *youtube.Client satisfies it.
type Source interface {
	RecentStreams(ctx context.Context, channelID string, pages int) (broadcast.Scan, error)
}

// Recorder keeps a history of the streams seen. *history.File satisfies it.
type Recorder interface {
	Record(broadcast.Stream) error
}

// Announcer takes the latest known state of a stream. *broadcast.Announcer satisfies it.
type Announcer interface {
	Update(broadcast.Stream) error
	Knows(videoID string) bool // is there already an announcement for it?
}

type Poller struct {
	source    Source
	announcer Announcer
	recorder  Recorder
	channelID string
	every     time.Duration
	log       *slog.Logger
	now       func() time.Time // a field so tests can set the clock

	// Only the goroutine running Run touches these, so they need no lock.
	checked bool            // has one check succeeded yet?
	warned  map[string]bool // notes already logged, so they are not repeated every time
}

func New(source Source, announcer Announcer, recorder Recorder, channelID string, every time.Duration, log *slog.Logger) *Poller {
	return &Poller{
		source: source, announcer: announcer, recorder: recorder, channelID: channelID,
		every: every, log: log, now: time.Now,
		warned: map[string]bool{},
	}
}

// Run checks right away (so a restart does not wait a whole interval), then once per interval, until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.check(ctx)

	ticker := time.NewTicker(p.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.check(ctx)
		}
	}
}

// check is one round: look at the channel, then pass on what is worth announcing. A failure only skips this round; the next one tries again.
func (p *Poller) check(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, checkTimeout)
	defer cancel()

	scan, err := p.source.RecentStreams(ctx, p.channelID, 1)
	if err != nil {
		if parent.Err() != nil {
			return // shutting down, not a failure
		}
		p.log.Warn("youtube check failed, will try again", "err", err, "in", p.every)
		return
	}
	for _, note := range scan.Notes {
		if !p.warned[note] {
			p.warned[note] = true
			p.log.Warn("youtube check", "note", note)
		}
	}

	now := p.now()
	passed := 0
	// YouTube lists newest first. Going from the end announces the oldest first, so several new streams appear in the order they were made.
	for i := len(scan.Streams) - 1; i >= 0; i-- {
		st := scan.Streams[i]
		if !worthAnnouncing(st, now, p.announcer.Knows(st.VideoID)) {
			continue
		}
		passed++
		if err := p.recorder.Record(st); err != nil {
			p.log.Warn("saving to the history failed, will try again", "video", st.VideoID, "err", err)
		}
		if err := p.announcer.Update(st); err != nil {
			p.log.Warn("announcing failed, will try again", "video", st.VideoID, "err", err)
		}
	}

	if !p.checked {
		p.checked = true
		p.log.Info("first youtube check done",
			"videos", scan.Checked, "streams", len(scan.Streams), "passed_on", passed, "every", p.every)
	} else {
		p.log.Debug("youtube check done", "videos", scan.Checked, "streams", len(scan.Streams), "passed_on", passed)
	}
}

// worthAnnouncing is the bot's policy on what counts. (Whether a stream that is already over gets a message is the announcer's decision, not ours.)
//
// known says the stream already has an announcement. Then it was judged to be
// a stream while it was still upcoming or live, which is more reliable than
// the guess made once it is over (a stream that just ended may not be on the
// Live tab list yet, and would look like a premiere). So it keeps being updated.
func worthAnnouncing(st broadcast.Stream, now time.Time, known bool) bool {
	if st.Kind == broadcast.KindPremiere && !known {
		return false // a video release, not a stream
	}
	if st.State == broadcast.Upcoming && !st.StartAt.IsZero() {
		if st.StartAt.After(now.Add(maxAhead)) {
			return false // placeholder room, too far ahead
		}
		if st.StartAt.Before(now.Add(-staleAfter)) {
			return false // abandoned room
		}
	}
	return true
}
