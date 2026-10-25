package broadcast

import (
	"sync"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Messenger is the part of Discord the announcer needs (the same two methods the heartbeat uses). discord.SessionMessenger satisfies it automatically.
type Messenger interface {
	Send(channelID, content string) (messageID string, err error)
	Edit(channelID, messageID, content string) error
}

// announced remembers what we already posted for one video.
type announced struct {
	messageID string
	state     State
	text      string
}

// Announcer posts ONE message per stream and edits that same message as the stream moves from upcoming to live to ended.
// It only remembers in memory, so after a restart it forgets what it posted (saving this in a database comes later).
type Announcer struct {
	m         Messenger
	channelID string

	mu     sync.Mutex           // Discord runs each command in its own goroutine, so the map needs a lock
	posted map[string]announced // by video ID
}

func NewAnnouncer(m Messenger, channelID string) *Announcer {
	return &Announcer{m: m, channelID: channelID, posted: map[string]announced{}}
}

// Update is the only method sources call. It can be called as often as you like with the latest known state: it posts, edits or does nothing, so one stream never produces two messages however many signals report it.
func (a *Announcer) Update(s Stream) error {
	a.mu.Lock()
	defer a.mu.Unlock() // note: the lock is held during the Discord call, fine at this size

	text := textFor(s)
	prev, seen := a.posted[s.VideoID]

	switch {
	case !seen && s.State == Ended:
		return nil // first seen when already over: nothing worth announcing

	case !seen:
		id, err := a.m.Send(a.channelID, text)
		if err != nil {
			return err // not recorded, so the next Update tries again
		}
		a.posted[s.VideoID] = announced{messageID: id, state: s.State, text: text}
		return nil

	case s.State < prev.state:
		return nil // stale information: streams only move forward

	case text == prev.text:
		return nil // nothing changed, so do not touch Discord

	default:
		if err := a.m.Edit(a.channelID, prev.messageID, text); err != nil {
			return err
		}
		a.posted[s.VideoID] = announced{messageID: prev.messageID, state: s.State, text: text}
		return nil
	}
}

func textFor(s Stream) string {
	switch s.State {
	case Live:
		return msg.StreamLive(s.Title, s.URL())
	case Ended:
		return msg.StreamEnded(s.Title, s.URL())
	default:
		return msg.StreamUpcoming(s.Title, s.URL(), s.StartAt)
	}
}
