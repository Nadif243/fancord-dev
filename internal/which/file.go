package which

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
)

const formatVersion = 1

// Status says whether a question still waits for an answer.
type Status string

const (
	Open Status = "open"
	Done Status = "done"
)

// Item is one question: one nickname in one stream's title.
type Item struct {
	Key        string    `json:"key"` // "<videoID>.<n>", also in the button IDs
	VideoID    string    `json:"videoId"`
	Title      string    `json:"title"`
	Start      time.Time `json:"start"`
	Word       string    `json:"word"`       // the nickname, lower-case
	Candidates []string  `json:"candidates"` // partners.csv names, as they were when asked

	MessageID string    `json:"messageId,omitempty"`
	Status    Status    `json:"status"`
	Chosen    []string  `json:"chosen,omitempty"` // empty after "neither"
	DecidedBy string    `json:"decidedBy,omitempty"`
	Asked     time.Time `json:"asked"`
	Decided   time.Time `json:"decided"`
}

type fileData struct {
	Version int             `json:"version"`
	Items   map[string]Item `json:"items"` // by key
}

// File keeps the questions in one JSON file, rewritten on every change.
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// ErrUnknown means there is no such question.
var ErrUnknown = errors.New("unknown question")

// OpenFile reads the file, creating it on first run. A file that cannot be understood is an error: carrying on with an empty one would ask everything again.
func OpenFile(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Items: map[string]Item{}}}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create the folder for %s: %w", path, err)
		}
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.save(); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", path, err)
		}
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%s cannot be read (%w); fix it or delete it to start fresh", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("%s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	if d.Items != nil {
		f.data.Items = d.Items
	}
	return f, nil
}

// Find returns the question about this nickname in this stream, if there is one.
func (f *File) Find(videoID, word string) (Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.data.Items {
		if it.VideoID == videoID && it.Word == word {
			return copyItem(it), true
		}
	}
	return Item{}, false
}

// Get returns a question by its key.
func (f *File) Get(key string) (Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.data.Items[key]
	return copyItem(it), ok
}

// Add stores a new question under the next free key of its stream. It is kept for this run even when saving fails, and the error says so.
func (f *File) Add(it Item) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.data.Items {
		if x.VideoID == it.VideoID {
			n++
		}
	}
	for {
		it.Key = fmt.Sprintf("%s.%d", it.VideoID, n)
		if _, taken := f.data.Items[it.Key]; !taken {
			break
		}
		n++
	}
	f.data.Items[it.Key] = copyItem(it)
	return copyItem(it), f.save()
}

// Update changes one question under the lock, so two people pressing at the same moment cannot both win. fn reports whether it changed anything; only then is the file written.
func (f *File) Update(key string, fn func(*Item) bool) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.data.Items[key]
	if !ok {
		return Item{}, ErrUnknown
	}
	it = copyItem(it)
	if !fn(&it) {
		return it, nil
	}
	f.data.Items[key] = copyItem(it)
	return it, f.save()
}

// Items returns every question, oldest first.
func (f *File) Items() []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Item, 0, len(f.data.Items))
	for _, it := range f.data.Items {
		out = append(out, copyItem(it))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Asked.Equal(out[j].Asked) {
			return out[i].Asked.Before(out[j].Asked)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// drop forgets a question that could not be posted, so the next scan asks again.
func (f *File) drop(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data.Items, key)
	_ = f.save()
}

func copyItem(it Item) Item {
	it.Candidates = slices.Clone(it.Candidates)
	it.Chosen = slices.Clone(it.Chosen)
	return it
}

func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", f.path, err)
	}
	return atomicfile.Write(f.path, raw)
}
