package broadcast

import (
	"fmt"
	"sync"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Messenger is the part of Discord the announcer needs (the same two methods the heartbeat uses). discord.SessionMessenger satisfies it automatically.
type Messenger interface {
	Send(channelID, content string) (messageID string, err error)
	Edit(channelID, messageID, content string) error

	// Ping sends a message that is allowed to mention one role (and nobody else).
	Ping(channelID, content, roleID string) error
}

// Announced is what we remember about the message posted for one stream.
type Announced struct {
	MessageID string
	State     State
	Text      string

	// ChannelID is where the message was posted, so it is still edited there if the announcement channel is changed meanwhile. Empty in records from before this was kept: the current channel is used for those.
	ChannelID string
}

// Memory is where the announcer keeps what it has posted. It is an interface so the real bot can use a file (package store) and tests a fake.
type Memory interface {
	Get(videoID string) (Announced, bool)

	// Put remembers a record. It must be remembered for the rest of this run even when the returned error says it could not be saved to disk.
	Put(videoID string, a Announced) error
}

// Announcer posts ONE message per stream and edits that same message as the stream moves from upcoming to live to ended.
type Announcer struct {
	m       Messenger
	channel func() string // where new announcements go, asked each time
	mem     Memory

	// The roles to ping, asked each time; "" means no ping.
	upcomingRole, liveRole func() string

	mu sync.Mutex // Discord runs each command in its own goroutine; one Update at a time
}

func NewAnnouncer(m Messenger, channelID string, mem Memory) *Announcer {
	return &Announcer{m: m, channel: constant(channelID), mem: mem, upcomingRole: constant(""), liveRole: constant("")}
}

func constant(s string) func() string { return func() string { return s } }

// WithChannel makes the announcer ask where to post each time (so the channel can be changed while the bot runs) instead of using the one it was made with.
func (a *Announcer) WithChannel(channel func() string) *Announcer {
	a.channel = channel
	return a
}

// WithPingFuncs is WithPings for roles that can change while the bot runs.
func (a *Announcer) WithPingFuncs(upcomingRole, liveRole func() string) *Announcer {
	a.upcomingRole, a.liveRole = upcomingRole, liveRole
	return a
}

// WithPings sets the roles to ping: upcomingRole when a new waiting room appears, liveRole when a stream goes live. An empty ID switches that ping off.
func (a *Announcer) WithPings(upcomingRole, liveRole string) *Announcer {
	a.upcomingRole, a.liveRole = constant(upcomingRole), constant(liveRole)
	return a
}

// Knows reports whether this stream already has an announcement.
func (a *Announcer) Knows(videoID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.mem.Get(videoID)
	return ok
}

// Update is the only method sources call. It can be called as often as you like with the latest known state: it posts, edits or does nothing, so one stream never produces two messages however many signals report it.
func (a *Announcer) Update(s Stream) error {
	a.mu.Lock()
	defer a.mu.Unlock() // note: the lock is held during the Discord call, fine at this size

	text := textFor(s)
	prev, seen := a.mem.Get(s.VideoID)

	switch {
	case !seen && s.State == Ended:
		return nil // first seen when already over: nothing worth announcing

	case !seen:
		ch := a.channel()
		id, err := a.m.Send(ch, text)
		if err != nil {
			return err // not remembered, so the next Update tries again
		}
		if err := a.remember(s, ch, id, text); err != nil {
			return err
		}
		return a.ping(s, ch)

	case s.State < prev.State:
		return nil // stale information: streams only move forward

	case text == prev.Text:
		return nil // nothing changed, so do not touch Discord

	default:
		ch := prev.ChannelID // where it was posted, even if the channel was changed since
		if ch == "" {
			ch = a.channel()
		}
		if err := a.m.Edit(ch, prev.MessageID, text); err != nil {
			return err
		}
		if err := a.remember(s, ch, prev.MessageID, text); err != nil {
			return err
		}
		if s.State != prev.State {
			return a.ping(s, ch)
		}
		return nil
	}
}

// ping mentions a role for the state the stream has just entered. It runs after the announcement is saved, so a failed ping is never retried: better one missed ping than the same ping twice.
func (a *Announcer) ping(s Stream, channelID string) error {
	upcoming, live := a.upcomingRole(), a.liveRole() // read once, so one ping uses one answer
	var text, role string
	switch {
	case s.Channel != "" && s.State == Upcoming && upcoming != "":
		role, text = upcoming, msg.PingCollabUpcoming(upcoming, s.Title, s.Channel, s.URL(), s.StartAt, s.Kind == KindPremiere)
	case s.Channel != "" && s.State == Live && live != "":
		role, text = live, msg.PingCollabLive(live, s.Title, s.Channel, s.URL(), s.Kind == KindPremiere)
	case s.Channel != "":
		return nil
	case s.State == Upcoming && upcoming != "":
		role, text = upcoming, msg.PingUpcoming(upcoming, s.Title, s.URL(), s.StartAt)
	case s.State == Live && live != "":
		role, text = live, msg.PingLive(live, s.Title, s.URL())
	default:
		return nil
	}
	if err := a.m.Ping(channelID, text, role); err != nil {
		return fmt.Errorf("announced %s but could not ping: %w", s.VideoID, err)
	}
	return nil
}

// remember records what is now on Discord. If saving fails the message is still posted, so the error says so.
func (a *Announcer) remember(s Stream, channelID, messageID, text string) error {
	rec := Announced{MessageID: messageID, State: s.State, Text: text, ChannelID: channelID}
	if err := a.mem.Put(s.VideoID, rec); err != nil {
		return fmt.Errorf("announced %s but could not save it: %w", s.VideoID, err)
	}
	return nil
}

func textFor(s Stream) string {
	if s.Channel != "" {
		premiere := s.Kind == KindPremiere
		switch s.State {
		case Live:
			return msg.CollabLive(s.Title, s.Channel, s.URL(), premiere)
		case Ended:
			return msg.CollabEnded(s.Title, s.Channel, s.URL(), premiere)
		default:
			return msg.CollabUpcoming(s.Title, s.Channel, s.URL(), s.StartAt, premiere)
		}
	}
	switch s.State {
	case Live:
		return msg.StreamLive(s.Title, s.URL())
	case Ended:
		return msg.StreamEnded(s.Title, s.URL())
	default:
		return msg.StreamUpcoming(s.Title, s.URL(), s.StartAt)
	}
}
