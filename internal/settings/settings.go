// Package settings holds the channels and roles that the fancord head can
// change from Discord with /setup, while the bot is running.
//
// Where a value comes from, strongest first:
//
//  1. what /setup saved (data/settings.json),
//  2. what .env says,
//  3. for some channels, another channel (the recap goes to the announcement
//     channel unless it has its own, and so on; see fallback).
//
// Everything that posts somewhere asks this package each time it posts, so a
// change shows up on the very next message, with no restart.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sync"

	"github.com/Nadif243/fancord-bot/internal/atomicfile"
)

// ErrRefused is wrapped in the error of a Set that was turned down (an unknown key, a value that is not a Discord ID). Any other error from Set means the value is in force but could not be written to disk.
var ErrRefused = errors.New("refused")

// Key names one thing that can be set.
type Key string

const (
	Announce  Key = "announce"  // stream announcements
	Tweets    Key = "tweets"    // her relayed X posts
	Recap     Key = "recap"     // the weekly recap
	Fans      Key = "fans"      // where fans paste collab links
	Review    Key = "review"    // collab candidates with the buttons
	Ops       Key = "ops"       // heartbeat and alerts
	Staff     Key = "staff"     // staff desk: /collab, dev commands
	Reminder  Key = "reminder"  // the Saturday reminder
	Ping      Key = "ping"      // the role pinged for waiting rooms and live streams
	TweetPing Key = "tweetping" // the role pinged for each of her relayed tweets
	Reviewer  Key = "reviewer"  // the role that may press the review buttons
)

// Kind says what sort of ID a key holds.
type Kind int

const (
	ChannelKind Kind = iota
	RoleKind
)

// Info describes a key for people: what it is and what it falls back to.
type Info struct {
	Key      Key
	Kind     Kind
	Label    string // short, English: shown in /setup
	Fallback Key    // the key used when this one is not set; "" for none
}

// All lists every key in the order /setup shows them.
var All = []Info{
	{Announce, ChannelKind, "Stream announcements", Staff},
	{Tweets, ChannelKind, "Tweets", Announce},
	{Recap, ChannelKind, "Weekly recap", Announce},
	{Fans, ChannelKind, "Fan collab links", ""},
	{Review, ChannelKind, "Collab review", Staff},
	{Ops, ChannelKind, "Ops (heartbeat and alerts)", Staff},
	{Staff, ChannelKind, "Staff desk (/collab and dev commands)", ""},
	{Reminder, ChannelKind, "Saturday reminder", Staff},
	{Ping, RoleKind, "Ping role (waiting room and live)", ""},
	{TweetPing, RoleKind, "Tweet ping role", ""},
	{Reviewer, RoleKind, "Reviewer role", ""},
}

// Lookup finds the description of a key.
func Lookup(k Key) (Info, bool) {
	for _, i := range All {
		if i.Key == k {
			return i, true
		}
	}
	return Info{}, false
}

var snowflake = regexp.MustCompile(`^\d{17,20}$`)

// Source says where an effective value came from.
type Source int

const (
	Unset     Source = iota // nothing anywhere
	FromSetup               // saved by /setup
	FromEnv                 // written in .env
	FromOther               // taken from another setting (see Info.Fallback)
)

const formatVersion = 1

type fileData struct {
	Version int            `json:"version"`
	Values  map[Key]string `json:"values"`
}

// Live is the set of current settings. It is safe to use from many goroutines.
type Live struct {
	mu   sync.RWMutex
	path string
	data fileData

	env map[Key]string // from .env; only the keys that were written there

	// The two old per-state ping roles in .env. They count only while /setup has not set the single ping role.
	envPingUpcoming, envPingLive string
}

// Env is what .env says, handed over once at start.
type Env struct {
	Values       map[Key]string // only keys that were actually written
	PingUpcoming string         // ROLE_PING_UPCOMING
	PingLive     string         // ROLE_PING_LIVE
}

// Open reads the settings file, creating it on first run. A file that cannot be understood is an error: starting empty would quietly forget the head's work.
func Open(path string, env Env) (*Live, error) {
	l := &Live{
		path: path, data: fileData{Version: formatVersion, Values: map[Key]string{}},
		env: map[Key]string{}, envPingUpcoming: env.PingUpcoming, envPingLive: env.PingLive,
	}
	for k, v := range env.Values {
		if v != "" {
			l.env[k] = v
		}
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := l.save(); err != nil {
			return nil, fmt.Errorf("cannot create the settings file: %w", err)
		}
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings file: %w", err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("settings file %s cannot be read (%w); fix it or delete it to go back to .env", path, err)
	}
	if d.Version != formatVersion {
		return nil, fmt.Errorf("settings file %s has format version %d, but this bot understands version %d", path, d.Version, formatVersion)
	}
	for k, v := range d.Values {
		// A key from a newer or older bot, or a hand-edited mistake, is skipped rather than trusted.
		if _, ok := Lookup(k); ok && snowflake.MatchString(v) {
			l.data.Values[k] = v
		}
	}
	return l, nil
}

// Get returns the value in force now, or "" when nothing is set.
func (l *Live) Get(k Key) string {
	v, _ := l.Where(k)
	return v
}

// Where is Get plus where the value came from.
func (l *Live) Where(k Key) (string, Source) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.where(k, 0)
}

func (l *Live) where(k Key, depth int) (string, Source) {
	if v := l.data.Values[k]; v != "" {
		return v, FromSetup
	}
	if v := l.env[k]; v != "" {
		return v, FromEnv
	}
	if i, ok := Lookup(k); ok && i.Fallback != "" && depth < len(All) { // depth: a loop in All must not hang the bot
		if v, src := l.where(i.Fallback, depth+1); src != Unset {
			return v, FromOther
		}
	}
	return "", Unset
}

// PingUpcoming is the role for a new waiting room, PingLive the role for a stream going live. /setup sets one role for both; the two old .env settings still work until it does.
func (l *Live) PingUpcoming() string { return l.pingFor(l.envPingUpcoming) }
func (l *Live) PingLive() string     { return l.pingFor(l.envPingLive) }

func (l *Live) pingFor(old string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if v := l.data.Values[Ping]; v != "" {
		return v
	}
	if old != "" {
		return old
	}
	return l.env[Ping]
}

// Set saves a value so it applies from now on. The value must be a Discord ID.
// If saving fails the value is still in force until the bot stops, and the error says so.
func (l *Live) Set(k Key, id string) error {
	if _, ok := Lookup(k); !ok {
		return fmt.Errorf("%w: unknown setting %q", ErrRefused, k)
	}
	if !snowflake.MatchString(id) {
		return fmt.Errorf("%w: %q is not a Discord ID", ErrRefused, id)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data.Values[k] == id {
		return nil
	}
	l.data.Values[k] = id
	return l.save()
}

// Reset forgets what /setup saved for the key, so .env (or the fallback) decides again. It reports whether there was anything to forget.
func (l *Live) Reset(k Key) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.data.Values[k]; !ok {
		return false, nil
	}
	delete(l.data.Values, k)
	return true, l.save()
}

// Saved reports whether /setup has saved a value for the key.
func (l *Live) Saved(k Key) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.data.Values[k]
	return ok
}

func (l *Live) save() error {
	raw, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	return atomicfile.Write(l.path, raw)
}
