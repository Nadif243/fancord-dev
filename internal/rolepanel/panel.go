// Package rolepanel is the role-choosing panel: one message with a heading and
// a row of buttons for each group of roles. Pressing a button gives the role,
// or takes it away again. In a group marked "one", choosing a role takes away
// the one chosen before.
//
// The groups, their roles, emoji and descriptions live in one JSON file that
// the /rolepanel command edits. Nothing in this package knows about Discord's
// library: it decides what the panel says and what a press changes, and the
// discord package does the posting.
package rolepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
)

const (
	formatVersion = 1
	// A message holds five rows of five buttons, and an embed 25 fields.
	MaxRoles = 25
	// Longest description kept (an embed field holds 1024; this reads well).
	MaxDesc = 200
)

// Role is one role in a group.
type Role struct {
	ID    string `json:"id"`
	Name  string `json:"name"` // refreshed from the server when the panel is published
	Emoji string `json:"emoji"`
	Desc  string `json:"desc"`
}

// Category is one group of roles: one message in the channel.
type Category struct {
	Key       string `json:"key"`   // what people type, lower-case without spaces
	Name      string `json:"name"`  // as it is called in the sheet
	Title     string `json:"title"` // the heading of the message
	One       bool   `json:"one"`   // only one role of the group at a time
	Roles     []Role `json:"roles"`
	ChannelID string `json:"channelId,omitempty"` // where its message is
	MessageID string `json:"messageId,omitempty"`
}

type fileData struct {
	Version    int        `json:"version"`
	ChannelID  string     `json:"channelId,omitempty"` // where the panel goes
	Categories []Category `json:"categories"`
}

// Store keeps the groups in one JSON file, rewritten on every change.
type Store struct {
	mu   sync.Mutex
	path string
	data fileData
}

// OpenStore reads the file. When there is none it is created from seed (the groups the bot starts with). A file that cannot be understood is an error: carrying on with an empty one would lose the setup.
func OpenStore(path string, seed []Category) (*Store, error) {
	st := &Store{path: path, data: fileData{Version: formatVersion, Categories: cloneCategories(seed)}}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create the folder for %s: %w", path, err)
		}
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := st.save(); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", path, err)
		}
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%s cannot be read (%w); fix it or delete it to start from the built-in setup", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("%s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	st.data = d
	return st, nil
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", s.path, err)
	}
	return atomicfile.Write(s.path, raw)
}

func cloneCategories(in []Category) []Category {
	out := make([]Category, len(in))
	for i, c := range in {
		c.Roles = slices.Clone(c.Roles)
		out[i] = c
	}
	return out
}

// Categories returns a copy of every group, in the order they are posted.
func (s *Store) Categories() []Category {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCategories(s.data.Categories)
}

// Channel is where the panel goes ("" until it was published once).
func (s *Store) Channel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.ChannelID
}

var (
	// ErrNoCategory: no group has that name.
	ErrNoCategory = errors.New("no such group")
	// ErrFull: the group already holds MaxRoles roles.
	ErrFull = errors.New("the group is full")
	// ErrBadEmoji: the text is not an emoji.
	ErrBadEmoji = errors.New("that is not an emoji")
	// ErrGone: a message (or a role) that the panel points at does not exist any more.
	ErrGone = errors.New("it does not exist any more")
)

// Key turns a name into what people type: "Interest 2" -> "interest2".
func Key(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DefaultTitle is the heading of a group called name. A trailing number is left out, so "Interest 2" is headed like "Interest".
func DefaultTitle(name string) string {
	words := strings.Fields(name)
	for len(words) > 1 {
		last := words[len(words)-1]
		if _, err := fmt.Sscanf(last, "%d", new(int)); err != nil || strings.Trim(last, "0123456789") != "" {
			break
		}
		words = words[:len(words)-1]
	}
	return "Silahkan Pilih " + strings.Join(words, " ") + " Roles!"
}

// SetCategory creates a group, or changes an existing one (found by its key).
// title "" keeps the title of an existing group, or makes the default one.
func (s *Store) SetCategory(name string, one bool, title string) (created bool, c Category, err error) {
	key := Key(name)
	if key == "" {
		return false, Category{}, ErrNoCategory
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(key)
	if i < 0 {
		if title == "" {
			title = DefaultTitle(name)
		}
		s.data.Categories = append(s.data.Categories, Category{Key: key, Name: strings.TrimSpace(name), Title: title, One: one})
		i, created = len(s.data.Categories)-1, true
	} else {
		cat := &s.data.Categories[i]
		cat.One = one
		if title != "" {
			cat.Title = title
		}
	}
	return created, cloneCategories(s.data.Categories[i : i+1])[0], s.save()
}

func (s *Store) index(key string) int {
	for i, c := range s.data.Categories {
		if c.Key == key {
			return i
		}
	}
	return -1
}

// Find returns the group that holds a role.
func (s *Store) Find(roleID string) (Category, Role, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Categories {
		for _, r := range c.Roles {
			if r.ID == roleID {
				c.Roles = slices.Clone(c.Roles)
				return c, r, true
			}
		}
	}
	return Category{}, Role{}, false
}

// SetRole puts a role in a group (found by its key or name), or changes it when it is already in one. A role that was in another group moves.
func (s *Store) SetRole(category string, r Role) (moved bool, err error) {
	if _, ok := ParseEmoji(r.Emoji); !ok {
		return false, ErrBadEmoji
	}
	r.Emoji = strings.TrimSpace(r.Emoji)
	r.Desc = shorten(strings.TrimSpace(r.Desc), MaxDesc)
	s.mu.Lock()
	defer s.mu.Unlock()
	to := s.index(Key(category))
	if to < 0 {
		return false, ErrNoCategory
	}
	from, at := -1, -1
	for i, c := range s.data.Categories {
		for j, x := range c.Roles {
			if x.ID == r.ID {
				from, at = i, j
			}
		}
	}
	switch {
	case from == to:
		s.data.Categories[to].Roles[at] = r
	case len(s.data.Categories[to].Roles) >= MaxRoles:
		return false, ErrFull
	default:
		if from >= 0 {
			c := &s.data.Categories[from]
			c.Roles = slices.Delete(c.Roles, at, at+1)
			moved = true
		}
		s.data.Categories[to].Roles = append(s.data.Categories[to].Roles, r)
	}
	return moved, s.save()
}

// RemoveRole takes a role off the panel and says what it was.
func (s *Store) RemoveRole(roleID string) (Role, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Categories {
		c := &s.data.Categories[i]
		for j, r := range c.Roles {
			if r.ID == roleID {
				c.Roles = slices.Delete(c.Roles, j, j+1)
				return r, true, s.save()
			}
		}
	}
	return Role{}, false, nil
}

// update changes the file under the lock; fn reports whether it changed anything.
func (s *Store) update(fn func(d *fileData) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fn(&s.data) {
		return nil
	}
	return s.save()
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
