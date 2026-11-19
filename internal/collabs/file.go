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
	// Checked says a person has looked at who was in this stream (even if the
	// answer was "nobody"). The Saturday reminder lists streams that are not.
	Checked bool `json:"checked,omitempty"`
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

// Checked reports whether a person has confirmed who was in the stream.
func (f *File) Checked(videoID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.Streams[videoID].Checked
}

// Add records that these partners were in the stream. A partner who was removed earlier is back.
func (f *File) Add(videoID string, names []string) error {
	return f.Apply(videoID, names, nil, false)
}

// Apply is what /collab does to one stream, in one step and with one save: partners are added, then partners are removed (so someone named in both ends up removed), and when check is true the stream is marked as looked at. A partner who was added earlier is gone again when removed, and one who was removed earlier is back when added. Nothing is saved when nothing changes. If saving fails the change is kept for this run, as in the other files.
func (f *File) Apply(videoID string, add, remove []string, check bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	before := f.data.Streams[videoID]
	t := tags{Added: slices.Clone(before.Added), Removed: slices.Clone(before.Removed), Checked: before.Checked || check}
	for _, n := range add {
		t.Added = appendOnce(t.Added, n)
		t.Removed = slices.DeleteFunc(t.Removed, func(x string) bool { return x == n })
	}
	for _, n := range remove {
		t.Removed = appendOnce(t.Removed, n)
		t.Added = slices.DeleteFunc(t.Added, func(x string) bool { return x == n })
	}
	if t.Checked == before.Checked && slices.Equal(t.Added, before.Added) && slices.Equal(t.Removed, before.Removed) {
		return nil
	}
	f.data.Streams[videoID] = t
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
