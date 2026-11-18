// Package history keeps a record of every stream the bot has seen, in a JSON file. The weekly recap is built from it, so it never has to ask YouTube about the past.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
	"github.com/Nadif243/fancord-bot/internal/broadcast"
)

const formatVersion = 1

// How a stream's state and kind are written in the file.
const (
	stateUpcoming = "upcoming"
	stateLive     = "live"
	stateEnded    = "ended"

	kindStream   = "" // the empty kind is a live stream, as in every entry from before kinds were kept
	kindPremiere = "premiere"
	kindVideo    = "video"
	kindShort    = "short"
)

// Entry is one stream as it looked the last time we saw it.
type Entry struct {
	VideoID string    `json:"videoId"`
	Title   string    `json:"title"`
	State   string    `json:"state"` // "upcoming", "live" or "ended"
	Start   time.Time `json:"start"` // zero when unknown; for a video or Short, when it was published
	End     time.Time `json:"end"`   // zero until it has ended

	// Kind is "" for a live stream, or "premiere", "video" or "short". Only streams count for the recap, the nickname questions and /collab; the rest is kept so the history says everything the bot announced.
	Kind string `json:"kind,omitempty"`
}

// IsStream says the entry is a live stream.
func (e Entry) IsStream() bool { return e.Kind == kindStream }

// Duration is how long the stream ran, or 0 when we do not know.
func (e Entry) Duration() time.Duration {
	if e.State != stateEnded || e.Start.IsZero() || e.End.IsZero() || e.End.Before(e.Start) {
		return 0
	}
	return e.End.Sub(e.Start)
}

type fileData struct {
	Version int              `json:"version"`
	Streams map[string]Entry `json:"streams"` // by YouTube video ID
}

// File is the history, backed by one JSON file that is rewritten whenever an entry changes (a few hundred streams a year is tiny).
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// Open reads the file, creating it on first run. A file that cannot be understood is an error: carrying on with an empty history would quietly lose the data the recaps depend on.
func Open(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Streams: map[string]Entry{}}}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create the folder for %s: %w", path, err)
		}
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.save(); err != nil {
			return nil, fmt.Errorf("cannot create the history file: %w", err)
		}
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history file: %w", err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("history file %s cannot be read (%w); fix it or delete it to start fresh", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("history file %s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	if d.Streams != nil {
		f.data.Streams = d.Streams
	}
	return f, nil
}

// Record stores the latest facts about a stream. Calling it with nothing new does not touch the disk. Information never gets worse: a stream that ended is not turned back into "live" by a late, stale report.
func (f *File) Record(s broadcast.Stream) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	e := Entry{
		VideoID: s.VideoID, Title: s.Title, State: stateName(s.State),
		Start: s.StartAt.UTC(), End: s.EndAt.UTC(), Kind: kindName(s.Kind),
	}
	if old, ok := f.data.Streams[s.VideoID]; ok {
		if stateRank(e.State) < stateRank(old.State) {
			return nil
		}
		// A stream that has just ended may not be on the Live tab yet and look like a premiere for a while. Once it was seen as a stream it stays one, or it would fall out of the recap.
		if old.IsStream() && e.Kind == kindPremiere {
			e.Kind = kindStream
		}
		if old.same(e) {
			return nil
		}
	}
	f.data.Streams[s.VideoID] = e
	return f.save()
}

// same says two entries hold the same facts. Times are compared with Equal, not ==, which would also compare the time zone and clock readings inside them.
func (e Entry) same(o Entry) bool {
	return e.VideoID == o.VideoID && e.Title == o.Title && e.State == o.State && e.Kind == o.Kind &&
		e.Start.Equal(o.Start) && e.End.Equal(o.End)
}

// Ended returns the streams that finished in [from, to), oldest first.
// "Finished" is judged by the moment a stream ended, not the moment it started: a stream that is still live when one recap is made then lands in the next one, and no stream can fall between two recaps. (A stream whose end time we never learnt is placed by its start time instead.)
func (f *File) Ended(from, to time.Time) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Entry
	for _, e := range f.data.Streams {
		at := e.finishedAt()
		if e.IsStream() && e.State == stateEnded && !at.IsZero() && !at.Before(from) && at.Before(to) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].finishedAt().Before(out[j].finishedAt()) })
	return out
}

// Week returns the streams a recap for [from, to) counts, oldest first.
//
// A stream belongs to the week it STARTED in (a Sunday-afternoon stream that runs past 20:00 is still that week's), and only once it has ended. A stream that started before the window belongs here too when it ended at or after carry: the previous recap went out before it was over, so it is not in it.
// (A stream whose start we never learnt is placed by its end.)
func (f *File) Week(from, to, carry time.Time) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Entry
	for _, e := range f.data.Streams {
		if !e.IsStream() || e.State != stateEnded {
			continue
		}
		start, end := e.startedAt(), e.finishedAt()
		if start.IsZero() {
			continue
		}
		inside := !start.Before(from) && start.Before(to)
		carried := start.Before(from) && !end.Before(carry)
		if inside || carried {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].startedAt(), out[j].startedAt()
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].VideoID < out[j].VideoID
	})
	return out
}

// Running returns the streams that are live and started in [since, before).
// The recap waits for these to end.
func (f *File) Running(since, before time.Time) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Entry
	for _, e := range f.data.Streams {
		if e.IsStream() && e.State == stateLive && !e.Start.IsZero() && !e.Start.Before(since) && e.Start.Before(before) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VideoID < out[j].VideoID })
	return out
}

// Get returns one stream by its YouTube video ID.
func (f *File) Get(videoID string) (Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.data.Streams[videoID]
	return e, ok
}

// Recent returns up to n streams that are live or finished (not waiting rooms), newest first by start time. It is what a person picks from when tagging a stream by hand.
func (f *File) Recent(n int) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Entry
	for _, e := range f.data.Streams {
		if e.IsStream() && e.State != stateUpcoming {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].startedAt(), out[j].startedAt()
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].VideoID < out[j].VideoID // a fixed order for equal times
	})
	if n >= 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// startedAt is when the stream began, falling back to when it ended.
func (e Entry) startedAt() time.Time {
	if !e.Start.IsZero() {
		return e.Start
	}
	return e.End
}

// finishedAt is when the stream ended, falling back to its start.
func (e Entry) finishedAt() time.Time {
	if !e.End.IsZero() {
		return e.End
	}
	return e.Start
}

func kindName(k broadcast.Kind) string {
	switch k {
	case broadcast.KindPremiere:
		return kindPremiere
	case broadcast.KindVideo:
		return kindVideo
	case broadcast.KindShort:
		return kindShort
	default:
		return kindStream
	}
}

func stateName(s broadcast.State) string {
	switch s {
	case broadcast.Upcoming:
		return stateUpcoming
	case broadcast.Live:
		return stateLive
	default:
		return stateEnded
	}
}

func stateRank(name string) int {
	switch name {
	case stateUpcoming:
		return 1
	case stateLive:
		return 2
	default:
		return 3
	}
}

// save rewrites the whole file (atomicfile writes a temporary file and renames it, so a crash never leaves half a file.
func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	if err := atomicfile.Write(f.path, raw); err != nil {
		return fmt.Errorf("save history: %w", err)
	}
	return nil
}
