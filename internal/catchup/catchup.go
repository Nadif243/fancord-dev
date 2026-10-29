// Package catchup tells the ops channel what happened while the bot was off.
//
// The bot has no memory of a stream it did not see, but it does not need any:
// the first YouTube check after a start looks at her latest videos again, so
// the history and the announcements catch up by themselves. What was missing
// is that nobody would know. This package remembers when the bot was last
// running, and at the next start posts "Back online" with the streams that
// ended meanwhile.
package catchup

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Marker remembers when the bot was last running. *store.File satisfies it.
type Marker interface {
	LastAlive() time.Time
	SetLastAlive(time.Time) error
}

// Streams is the stream history. *history.File satisfies it.
type Streams interface {
	Ended(from, to time.Time) []history.Entry
}

// Announcements says whether a stream has a message in the announcement channel. *broadcast.Announcer satisfies it.
type Announcements interface {
	Knows(videoID string) bool
}

// Sender posts a message. The Discord messenger satisfies it.
type Sender interface {
	Send(channelID, content string) (messageID string, err error)
}

const (
	// How often "I am running" is written down. After a crash or a power cut the "off since" time can be this much too early; after a normal stop it is exact, because the last write happens on the way out.
	aliveEvery = 5 * time.Minute

	// Being off for this long is worth a note even when no stream ended.
	reportGap = time.Hour
)

// Reporter writes down that the bot is running, and reports once per start.
type Reporter struct {
	marker  Marker
	streams Streams
	ann     Announcements
	send    Sender
	opsID   string
	log     *slog.Logger
	now     func() time.Time

	// prev is when the bot was last running before THIS start. It is read once in New, before Run starts overwriting the marker.
	prev time.Time
	once sync.Once
}

// New makes a Reporter. opsID is the channel for the note; empty means the note is only logged.
func New(marker Marker, streams Streams, ann Announcements, send Sender, opsID string, log *slog.Logger) *Reporter {
	return &Reporter{
		marker: marker, streams: streams, ann: ann, send: send, opsID: opsID, log: log,
		now:  time.Now,
		prev: marker.LastAlive(),
	}
}

// Run keeps the "last running" time fresh until ctx ends, and writes it one more time on the way out.
func (r *Reporter) Run(ctx context.Context) {
	r.beat()
	ticker := time.NewTicker(aliveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.beat()
			return
		case <-ticker.C:
			r.beat()
		}
	}
}

func (r *Reporter) beat() {
	if err := r.marker.SetLastAlive(r.now()); err != nil {
		r.log.Warn("could not save that the bot is running", "err", err)
	}
}

// FirstCheckDone is called after the first YouTube check that worked, when the history is up to date. Only the first call does anything.
func (r *Reporter) FirstCheckDone() { r.once.Do(r.report) }

func (r *Reporter) report() {
	if r.prev.IsZero() {
		r.log.Info("first run with the downtime note: nothing to compare with yet")
		return
	}
	now := r.now()
	gap := now.Sub(r.prev)
	if gap < 0 {
		return // the clock went backwards; there is nothing sensible to say
	}

	var missed []msg.MissedStream
	notAnnounced := 0
	for _, e := range r.streams.Ended(r.prev, now) {
		announced := r.ann.Knows(e.VideoID)
		if !announced {
			notAnnounced++
		}
		missed = append(missed, msg.MissedStream{Stream: stream(e), Announced: announced})
	}
	r.log.Info("back online", "off_for", gap.Round(time.Second).String(), "ended_meanwhile", len(missed), "not_announced", notAnnounced)

	if r.opsID == "" || (len(missed) == 0 && gap < reportGap) {
		return // a quick restart (an update, say) is not worth a message
	}
	for _, text := range msg.BackOnline(r.prev, now, missed) {
		if _, err := r.send.Send(r.opsID, text); err != nil {
			r.log.Warn("could not post the back-online note", "err", err)
			return
		}
	}
}

// stream describes a history entry for a message.
func stream(e history.Entry) msg.RecapStream {
	start := e.Start
	if start.IsZero() {
		start = e.End
	}
	return msg.RecapStream{
		Title:    e.Title,
		URL:      "https://www.youtube.com/watch?v=" + e.VideoID,
		Start:    start,
		Duration: e.Duration(),
	}
}
