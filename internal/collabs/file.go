// Package collabs decides who was in a stream with Yuura.
//
// Her titles often do not say, so two sources are combined: the partner list reads names out of the title (see package partners), and people who watched add or remove partners by hand. The result for a stream is
//
//	(named in the title + added by hand) - removed by hand
package collabs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
)

const formatVersion = 1

// tags are the by-hand changes for one stream.
type tags struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

type fileData struct {
	Version int             `json:"version"`
	Streams map[string]tags `json:"streams"` // by YouTube video ID
}

// File keeps the by-hand tags in one JSON file, rewritten on every change.
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// OpenFile reads the file, creating it on first run. A file that cannot be understood is an error: starting empty would quietly drop everyone's work.
func OpenFile(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Streams: map[string]tags{}}}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.save(); err != nil {
			return nil, fmt.Errorf("cannot create the collab tags file: %w", err)
		}
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read collab tags file: %w", err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("collab tags file %s cannot be read (%w); fix it or delete it to start fresh", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("collab tags file %s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	if d.Streams != nil {
		f.data.Streams = d.Streams
	}
	return f, nil
}

// Get returns the partners added and removed by hand for a stream.
func (f *File) Get(videoID string) (added, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.data.Streams[videoID]
	return slices.Clone(t.Added), slices.Clone(t.Removed)
}

// Add records that these partners were in the stream. A partner who was removed earlier is back.
func (f *File) Add(videoID string, names []string) error {
	return f.change(videoID, names, func(t *tags, n string) bool {
		changed := !slices.Contains(t.Added, n) || slices.Contains(t.Removed, n)
		t.Added = appendOnce(t.Added, n)
		t.Removed = slices.DeleteFunc(t.Removed, func(x string) bool { return x == n })
		return changed
	})
}

// Remove records that these partners were not in the stream (even if the title names them). A partner who was added earlier is gone again.
func (f *File) Remove(videoID string, names []string) error {
	return f.change(videoID, names, func(t *tags, n string) bool {
		changed := !slices.Contains(t.Removed, n) || slices.Contains(t.Added, n)
		t.Removed = appendOnce(t.Removed, n)
		t.Added = slices.DeleteFunc(t.Added, func(x string) bool { return x == n })
		return changed
	})
}

// change applies edit to every name and saves once, only if anything changed.
// If saving fails the change is kept for this run, as in the other files.
func (f *File) change(videoID string, names []string, edit func(*tags, string) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	t := f.data.Streams[videoID]
	changed := false
	for _, n := range names {
		if edit(&t, n) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(t.Added) == 0 && len(t.Removed) == 0 {
		delete(f.data.Streams, videoID)
	} else {
		f.data.Streams[videoID] = t
	}
	return f.save()
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode collab tags: %w", err)
	}
	return atomicfile.Write(f.path, raw)
}
