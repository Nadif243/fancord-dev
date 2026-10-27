// Package store keeps the bot's memory in a JSON file, so that a restart does not make it forget what it already announced.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
)

// formatVersion lets a future version of the bot recognise an old file.
const formatVersion = 1

// record is how one announcement looks inside the file. It is separate from broadcast.Announced so the file format does not change by accident when that type does.
type record struct {
	MessageID string `json:"messageId"`
	State     int    `json:"state"`
	Text      string `json:"text"`
}

type fileData struct {
	Version   int               `json:"version"`
	Announced map[string]record `json:"announced"` // by YouTube video ID

	// LastRecap is the end of the last weekly recap window that was posted.
	// It is how a restart knows the recap of this week is already out.
	LastRecap time.Time `json:"lastRecap"`
}

// File is a broadcast.Memory backed by one JSON file. The whole file is rewritten on every change, which is fine at this size (a few hundred streams a year).
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// Open reads the file. A file that does not exist yet is fine (first run) and is created right away, so a folder we cannot write to is noticed at startup instead of at the first announcement. A file that cannot be understood is an error, because carrying on with an empty memory would announce every stream a second time.
func Open(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Announced: map[string]record{}}}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.save(); err != nil {
			return nil, fmt.Errorf("cannot create the state file: %w", err)
		}
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("state file %s cannot be read (%w); fix it or delete it to start fresh", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("state file %s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	if d.Announced != nil {
		f.data.Announced = d.Announced
	}
	f.data.LastRecap = d.LastRecap
	return f, nil
}

func (f *File) Get(videoID string) (broadcast.Announced, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.data.Announced[videoID]
	return broadcast.Announced{MessageID: r.MessageID, State: broadcast.State(r.State), Text: r.Text}, ok
}

// Put remembers the record and then saves the file. If saving fails the record is still remembered for this run, and the error is returned.
func (f *File) Put(videoID string, a broadcast.Announced) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Announced[videoID] = record{MessageID: a.MessageID, State: int(a.State), Text: a.Text}
	return f.save()
}

// LastRecap is the end of the last recap window that was posted; the zero time when no recap has been handled yet.
func (f *File) LastRecap() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.LastRecap
}

// SetLastRecap remembers that the recap for the window ending at t is done.
// As with Put, the value is kept for this run even if saving fails.
func (f *File) SetLastRecap(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.LastRecap = t.UTC()
	return f.save()
}

// save writes to a temporary file next to the real one and then renames it over the real one. A crash halfway through therefore leaves the old file intact, never half a file.
func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("save state: %w", err)
	}
	if err := tmp.Sync(); err != nil { // make sure the bytes are on disk before the rename
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("save state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("save state: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		cleanup()
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
