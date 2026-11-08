// Package rodok counts how often one person has "merodok" another, and keeps
// people from spamming the command.
//
// The counts are per pair: Ana rodok-ing Budi 3 times and Ana rodok-ing Citra
// once are two separate numbers, and Budi rodok-ing Ana starts from zero.
package rodok

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
)

const formatVersion = 1

type fileData struct {
	Version int `json:"version"`
	// Counts[actor][target] is how many times actor has rodok-ed target.
	// Both are Discord user IDs.
	Counts map[string]map[string]int `json:"counts"`
}

// File keeps the counts in one JSON file, rewritten on every change.
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// OpenFile reads the file, creating it on first run. A file that cannot be understood is an error: starting empty would quietly drop every count.
func OpenFile(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Counts: map[string]map[string]int{}}}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.save(); err != nil {
			return nil, fmt.Errorf("cannot create the rodok file: %w", err)
		}
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rodok file: %w", err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("rodok file %s cannot be read (%w); fix it or delete it to start fresh", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("rodok file %s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	if d.Counts != nil {
		f.data.Counts = d.Counts
	}
	return f, nil
}

// Add counts one more time that actor rodok-ed target and returns the new total for that pair. If saving fails the count is still kept for this run (as in the other files) and the error is returned next to it.
func (f *File) Add(actor, target string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	row := f.data.Counts[actor]
	if row == nil {
		row = map[string]int{}
		f.data.Counts[actor] = row
	}
	row[target]++
	return row[target], f.save()
}

// Count returns how many times actor has rodok-ed target so far.
func (f *File) Count(actor, target string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.Counts[actor][target]
}

func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rodok counts: %w", err)
	}
	return atomicfile.Write(f.path, raw)
}
