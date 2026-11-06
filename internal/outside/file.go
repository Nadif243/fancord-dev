// Package outside handles collabs that happen on OTHER channels: a video on a partner's channel that she takes part in.
//
// Candidates come from links in her X posts (see package relay) and from
// people sending links with /collablink. Each new video is looked up on
// YouTube and, unless it is obviously not wanted, posted to the review
// channel, where someone with the reviewer role confirms or ignores it.
package outside

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

// Status says where a video is in the review.
type Status string

const (
	Pending   Status = "pending"   // waiting for a reviewer
	Confirmed Status = "confirmed" // a reviewer says she took part
	Ignored   Status = "ignored"   // not wanted, by a reviewer or by a rule
)

// Item is one video that was looked at.
type Item struct {
	VideoID      string `json:"videoId"`
	Title        string `json:"title"`
	ChannelID    string `json:"channelId"`
	ChannelTitle string `json:"channelTitle"`

	State broadcast.State `json:"state"` // 0: an ordinary upload, not a stream
	Kind  broadcast.Kind  `json:"kind"`
	Start time.Time       `json:"start"` // zero when unknown
	End   time.Time       `json:"end"`   // zero until it has ended

	Mentions bool     `json:"mentions"` // the title or description names her
	Partners []string `json:"partners"` // partners.csv names that match

	Source    string `json:"source"`    // "tweet" or "manual"
	SourceRef string `json:"sourceRef"` // the X post ID, or who sent it

	Status    Status    `json:"status"`
	Reason    string    `json:"reason,omitempty"`    // why it was ignored
	MessageID string    `json:"messageId,omitempty"` // the review message
	Added     time.Time `json:"added"`
	Decided   time.Time `json:"decided"`
	DecidedBy string    `json:"decidedBy,omitempty"`

	// After a confirmation: Done means nothing more will be announced or updated for this video. Missing counts rechecks in a row that did not find the video (deleted or made private).
	Done    bool `json:"done,omitempty"`
	Missing int  `json:"missing,omitempty"`
}

// TweetLook remembers what a fan-sent X post held, so sending the same post again does not cost another read.
type TweetLook struct {
	VideoIDs []string  `json:"videoIds"`
	At       time.Time `json:"at"`
}

// XReads counts the billed reads this feature used in one month.
type XReads struct {
	Month string `json:"month"` // "2026-11"
	N     int    `json:"n"`
}

const tweetMemory = 90 * 24 * time.Hour

type fileData struct {
	Version         int                  `json:"version"`
	Items           map[string]Item      `json:"items"`           // by YouTube video ID
	IgnoredChannels map[string]string    `json:"ignoredChannels"` // channel ID -> name
	Tweets          map[string]TweetLook `json:"tweets,omitempty"`
	XReads          XReads               `json:"xReads"`
}

// File keeps the items in one JSON file, rewritten on every change (a few hundred small entries a year).
type File struct {
	mu   sync.Mutex
	path string
	data fileData
}

// ErrUnknown means there is no such video in the file.
var ErrUnknown = errors.New("unknown video")

// OpenFile reads the file, creating it on first run. A file that cannot be understood is an error: carrying on with an empty one would show every old candidate to the reviewers again.
func OpenFile(path string) (*File, error) {
	f := &File{path: path, data: fileData{Version: formatVersion, Items: map[string]Item{}, IgnoredChannels: map[string]string{}, Tweets: map[string]TweetLook{}}}
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
	if d.IgnoredChannels != nil {
		f.data.IgnoredChannels = d.IgnoredChannels
	}
	if d.Tweets != nil {
		f.data.Tweets = d.Tweets
	}
	f.data.XReads = d.XReads
	return f, nil
}

// Get returns the item for a video.
func (f *File) Get(videoID string) (Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.data.Items[videoID]
	return copyItem(it), ok
}

// Put stores an item (replacing any with the same video ID). It is kept for this run even when saving fails, and the error says so.
func (f *File) Put(it Item) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Items[it.VideoID] = copyItem(it)
	return f.save()
}

// Update changes one item under the lock, so two reviewers clicking at the same moment cannot both win. fn reports whether it changed anything; only then is the file written.
func (f *File) Update(videoID string, fn func(*Item) bool) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.data.Items[videoID]
	if !ok {
		return Item{}, ErrUnknown
	}
	it = copyItem(it)
	if !fn(&it) {
		return it, nil
	}
	f.data.Items[videoID] = copyItem(it)
	return it, f.save()
}

// Items returns every item, oldest first.
func (f *File) Items() []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Item, 0, len(f.data.Items))
	for _, it := range f.data.Items {
		out = append(out, copyItem(it))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Added.Equal(out[j].Added) {
			return out[i].Added.Before(out[j].Added)
		}
		return out[i].VideoID < out[j].VideoID
	})
	return out
}

// IgnoreChannel makes every later video of this channel be skipped.
func (f *File) IgnoreChannel(channelID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.data.IgnoredChannels[channelID]; ok && old == name {
		return nil
	}
	f.data.IgnoredChannels[channelID] = name
	return f.save()
}

// ChannelIgnored reports whether a reviewer ignored this channel for good.
func (f *File) ChannelIgnored(channelID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data.IgnoredChannels[channelID]
	return ok
}

// Tweet returns what an earlier look at this X post found.
func (f *File) Tweet(id string) (TweetLook, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.data.Tweets[id]
	t.VideoIDs = append([]string(nil), t.VideoIDs...)
	return t, ok
}

// PutTweet remembers an X post that was read (at the given time). Posts read more than 90 days ago are forgotten.
func (f *File) PutTweet(id string, videoIDs []string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.data.Tweets {
		if at.Sub(t.At) > tweetMemory {
			delete(f.data.Tweets, k)
		}
	}
	f.data.Tweets[id] = TweetLook{VideoIDs: append([]string(nil), videoIDs...), At: at.UTC()}
	return f.save()
}

// XReadsIn is how many billed X reads were used in a month ("2026-11").
func (f *File) XReadsIn(month string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data.XReads.Month != month {
		return 0
	}
	return f.data.XReads.N
}

// AddXRead counts one billed read in a month and returns the new total.
func (f *File) AddXRead(month string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data.XReads.Month != month {
		f.data.XReads = XReads{Month: month}
	}
	f.data.XReads.N++
	return f.data.XReads.N, f.save()
}

func copyItem(it Item) Item {
	it.Partners = append([]string(nil), it.Partners...)
	return it
}

func (f *File) save() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", f.path, err)
	}
	return atomicfile.Write(f.path, raw)
}
