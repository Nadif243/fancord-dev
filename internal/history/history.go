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

	"github.com/Nadif243/fancord-bot/internal/broadcast"
)

const formatVersion = 1

// Entry is one stream as it looked the last time we saw it.
type Entry struct {
	VideoID string    `json:"videoId"`
	Title   string    `json:"title"`
	State   string    `json:"state"` // "upcoming", "live" or "ended"
	Start   time.Time `json:"start"` // zero when unknown
	End     time.Time `json:"end"`   // zero until it has ended
}

// Duration is how long the stream ran, or 0 when we do not know.
func (e Entry) Duration() time.Duration {
	if e.State != "ended" || e.Start.IsZero() || e.End.IsZero() || e.End.Before(e.Start) {
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

	e := Entry{VideoID: s.VideoID, Title: s.Title, State: stateName(s.State), Start: s.StartAt.UTC(), End: s.EndAt.UTC()}
	if s.StartAt.IsZero() {
		e.Start = time.Time{}
	}
	if s.EndAt.IsZero() {
		e.End = time.Time{}
	}

	if old, ok := f.data.Streams[s.VideoID]; ok {
		if stateRank(e.State) < stateRank(old.State) {
			return nil
		}
		if old == e {
			return nil
		}
	}
	f.data.Streams[s.VideoID] = e
	return f.save()
}

// Ended returns the finished streams that started in [from, to), oldest first.
func (f *File) Ended(from, to time.Time) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Entry
	for _, e := range f.data.Streams {
		if e.State == "ended" && !e.Start.IsZero() && !e.Start.Before(from) && e.Start.Before(to) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func stateName(s broadcast.State) string {
	switch s {
	case broadcast.Upcoming:
		return "upcoming"
	case broadcast.Live:
		return "live"
	default:
		return "ended"
	}
}

func stateRank(name string) int {
	switch name {
	case "upcoming":
		return 1
	case "live":
		return 2
	default:
		return 3
	}
}

// save writes a temporary file and renames it over the real one, so a crash never leaves half a file.
func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("save history: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("save history: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("save history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("save history: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		cleanup()
		return fmt.Errorf("save history: %w", err)
	}
	return nil
}
