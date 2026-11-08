// Package relay copies her X posts into a Discord channel.
//
// It checks X on a timer for posts newer than the last one it handled, posts
// a link for each into the tweets channel, and keeps every YouTube link it
// sees in them for the collab work. X bills per post read, so the relay counts
// what it reads and stops at a monthly cap.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/xapi"
)

// Link is a YouTube video found in one of her posts (or in the post it reposts or quotes).
type Link struct {
	PostID  string    `json:"postId"`
	VideoID string    `json:"videoId"`
	Found   time.Time `json:"found"`
}

// State is what the relay remembers between runs. *store.File keeps it.
type State struct {
	Username   string `json:"username"`   // the handle the IDs below belong to
	UserID     string `json:"userId"`     // looked up once (it costs a read)
	LastPostID string `json:"lastPostId"` // the newest post handled

	ReadsMonth string `json:"readsMonth"` // "2026-11"
	Reads      int    `json:"reads"`      // billed reads so far this month

	Links []Link `json:"links,omitempty"`
}

// maxLinks is how many links are kept; the oldest go first.
const maxLinks = 500

// Store keeps the relay's State. *store.File satisfies it.
type Store interface {
	Relay() State
	SetRelay(State) error
}

// Source is X. *xapi.Client satisfies it.
type Source interface {
	UserID(ctx context.Context, username string) (string, error)
	Posts(ctx context.Context, userID, sinceID string) (xapi.Page, error)
}

// Sender posts a message. The Discord messenger satisfies it.
type Sender interface {
	Send(channelID, content string) (messageID string, err error)
}

// Pinger posts a message that mentions one role. The Discord messenger satisfies it too; the relay uses it when a tweet ping role is set.
type Pinger interface {
	Ping(channelID, content, roleID string) error
}

// Settings are what the relay is configured with.
type Settings struct {
	Username  string // her handle, without the @
	ChannelID string // where the posts go
	OpsID     string // where warnings go; empty: log only

	// When set, these are asked each time instead of the two fixed IDs above, so the channels can be changed while the bot runs.
	ChannelFn, OpsFn func() string

	// RoleFn, when set, is asked each time for the role to ping with each relayed post. Empty answer (or no RoleFn): no ping.
	RoleFn     func() string
	Every      time.Duration // how often to check X
	Kinds      []xapi.Kind   // which post types to relay
	MonthlyCap int           // billed reads per month; 0 means no cap
}

const (
	requestTimeout = 30 * time.Second
	// A run of failures this long is worth a message in ops.
	alertAfterFailures = 3

	// X answers 402 when the account has no credits. Asking again every few minutes cannot help, so the relay waits this long between tries.
	noCreditsStatus = 402
	noCreditsPause  = time.Hour
	// Warn in ops when this share of the monthly cap is used.
	warnAt = 0.8
)

// Relay is the service. Only one goroutine (the one running Run) uses it.
type Relay struct {
	src   Source
	store Store
	send  Sender
	set   Settings
	log   *slog.Logger
	now   func() time.Time

	failures    int
	alerted     bool
	pausedUntil time.Time // after "no credits": no checks before this moment
	warned      string    // the month the 80% warning was sent for
	capNoted    string    // the month the cap message was sent for

	onLinks func(postID string, videoIDs []string)
	pinger  Pinger // the sender, when it can also ping; nil otherwise
}

func New(src Source, st Store, send Sender, set Settings, log *slog.Logger) *Relay {
	r := &Relay{src: src, store: st, send: send, set: set, log: log, now: time.Now}
	r.pinger, _ = send.(Pinger)
	return r
}

// announce posts one relayed post, pinging the tweet role when one is set.
func (r *Relay) announce(kind, postURL string) error {
	channel := r.channel()
	if r.set.RoleFn != nil && r.pinger != nil {
		if role := r.set.RoleFn(); role != "" {
			return r.pinger.Ping(channel, msg.TweetRelayPing(role, kind, postURL), role)
		}
	}
	_, err := r.send.Send(channel, msg.TweetRelay(kind, postURL))
	return err
}

// OnLinks sets a function that is called with the YouTube videos found in a post, after the post was handled and saved. Set it before Run.
func (r *Relay) OnLinks(f func(postID string, videoIDs []string)) { r.onLinks = f }

// Run checks once at the start, then every interval, until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	r.log.Info("tweet relay is on", "account", "@"+r.set.Username, "channel", r.channel(),
		"every", r.set.Every, "kinds", r.set.Kinds, "monthly_read_cap", r.set.MonthlyCap)
	r.Check(ctx)
	ticker := time.NewTicker(r.set.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Check(ctx)
		}
	}
}

// Check does one round. A failure only skips the round; the next one tries again.
func (r *Relay) Check(parent context.Context) {
	if r.now().Before(r.pausedUntil) {
		return // waiting for credits to be added
	}
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()

	if err := r.round(ctx); err != nil {
		if parent.Err() != nil {
			return // shutting down
		}
		r.failures++
		if hasStatus(err, noCreditsStatus) {
			r.pausedUntil = r.now().Add(noCreditsPause)
			r.log.Warn("tweet relay: X says there are no credits; trying again in an hour (restart the bot to try sooner)", "err", err)
			if !r.alerted {
				r.alerted = true
				r.ops(msg.RelayNoCredits())
			}
			return
		}
		r.log.Warn("tweet relay: check failed, will try again", "err", err, "failures", r.failures, "in", r.set.Every)
		if r.failures >= alertAfterFailures && !r.alerted {
			r.alerted = true
			r.ops(msg.RelayFailing(r.failures, err.Error()))
		}
		return
	}
	r.failures, r.alerted = 0, false
}

func (r *Relay) round(ctx context.Context) error {
	st := r.store.Relay()
	month := r.now().UTC().Format("2006-01")
	if st.ReadsMonth != month {
		st.ReadsMonth, st.Reads = month, 0
	}
	if r.set.MonthlyCap > 0 && st.Reads >= r.set.MonthlyCap {
		if r.capNoted != month {
			r.capNoted = month
			r.log.Warn("tweet relay: monthly read cap reached; paused until next month", "reads", st.Reads, "cap", r.set.MonthlyCap)
			r.ops(msg.RelayCapReached(st.Reads, r.set.MonthlyCap))
		}
		return nil
	}

	// A different handle means different IDs: start over.
	if st.Username != r.set.Username {
		st = State{Username: r.set.Username, ReadsMonth: month, Reads: st.Reads, Links: st.Links}
	}
	if st.UserID == "" {
		id, err := r.src.UserID(ctx, r.set.Username)
		if err != nil {
			return err // a refused call is not billed, so it is not counted
		}
		st.Reads += 2 // a user read costs $0.01, twice a post read
		st.UserID = id
		if err := r.save(st); err != nil {
			return err
		}
	}

	page, err := r.src.Posts(ctx, st.UserID, st.LastPostID)
	if err != nil {
		return err // nothing came back, so nothing was billed
	}
	st.Reads += page.Billed
	if err := r.save(st); err != nil {
		return err
	}

	if st.LastPostID == "" {
		// First look: remember where "now" is and relay nothing old.
		if n := len(page.Posts); n > 0 {
			st.LastPostID = page.Posts[n-1].ID
			r.log.Info("tweet relay ready: starting after her latest post", "post", st.LastPostID)
			if err := r.save(st); err != nil {
				return err
			}
		}
		return r.afterReads(st)
	}

	for _, p := range page.Posts {
		if !xapi.Newer(p.ID, st.LastPostID) {
			continue // the API should not repeat, but a repeat must not be posted twice
		}
		if slices.Contains(r.set.Kinds, p.Kind) {
			if err := r.announce(string(p.Kind), r.postURL(p.ID)); err != nil {
				return fmt.Errorf("post %s to Discord: %w", p.ID, err) // not marked handled: tried again next round
			}
		}
		st.LastPostID = p.ID
		st.Links = appendLinks(st.Links, p, r.now())
		if err := r.save(st); err != nil {
			return err
		}
		if ids := linkIDs(p); len(ids) > 0 && r.onLinks != nil {
			r.onLinks(p.ID, ids)
		}
	}
	return r.afterReads(st)
}

// afterReads warns in ops when the month's reads get close to the cap.
func (r *Relay) afterReads(st State) error {
	if cap := r.set.MonthlyCap; cap > 0 && float64(st.Reads) >= warnAt*float64(cap) && r.warned != st.ReadsMonth {
		r.warned = st.ReadsMonth
		r.ops(msg.RelayBudgetWarning(st.Reads, cap))
	}
	return nil
}

// hasStatus reports whether err is X answering with this HTTP status.
func hasStatus(err error, status int) bool {
	var s interface{ Status() int }
	return errors.As(err, &s) && s.Status() == status
}

func (r *Relay) save(st State) error {
	if err := r.store.SetRelay(st); err != nil {
		return fmt.Errorf("save relay state: %w", err)
	}
	return nil
}

func (r *Relay) channel() string {
	if r.set.ChannelFn != nil {
		return r.set.ChannelFn()
	}
	return r.set.ChannelID
}

func (r *Relay) ops(text string) {
	opsID := r.set.OpsID
	if r.set.OpsFn != nil {
		opsID = r.set.OpsFn()
	}
	if opsID == "" {
		return
	}
	if _, err := r.send.Send(opsID, text); err != nil {
		r.log.Warn("tweet relay: could not post the ops note", "err", err)
	}
}

func (r *Relay) postURL(id string) string {
	return "https://x.com/" + r.set.Username + "/status/" + id
}

// linkIDs lists the YouTube videos linked in a post, once each.
func linkIDs(p xapi.Post) []string {
	var ids []string
	for _, raw := range p.URLs {
		if id, ok := YouTubeID(raw); ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// appendLinks adds the YouTube videos found in the post, once each per post, and keeps at most maxLinks.
func appendLinks(links []Link, p xapi.Post, now time.Time) []Link {
	for _, id := range linkIDs(p) {
		links = append(links, Link{PostID: p.ID, VideoID: id, Found: now.UTC()})
	}
	if over := len(links) - maxLinks; over > 0 {
		links = append([]Link(nil), links[over:]...)
	}
	return links
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// YouTubeID reads a video ID from a YouTube link: watch?v=, youtu.be/, /live/, /shorts/ and /embed/. It reports false for anything else (a channel, a playlist, another site).
func YouTubeID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var id string
	switch {
	case host == "youtu.be":
		id = parts[0]
	case host == "youtube.com" || host == "music.youtube.com":
		switch {
		case parts[0] == "watch":
			id = u.Query().Get("v")
		case len(parts) >= 2 && (parts[0] == "live" || parts[0] == "shorts" || parts[0] == "embed"):
			id = parts[1]
		}
	}
	return id, videoID.MatchString(id)
}
