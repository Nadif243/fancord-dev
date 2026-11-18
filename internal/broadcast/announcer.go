package broadcast

import (
	"errors"
	"fmt"
	"strings"
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

// ErrGone is what Messenger.Edit returns (wrapped) when the message, or its channel, no longer exists: somebody deleted it.
var ErrGone = errors.New("the message is gone")

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

// Lister is a Memory that can say which streams it holds. The Memory of the real bot and of /devstream both can; Waiting needs it.
type Lister interface {
	IDs() []string
}

// Announcer posts ONE message per stream and edits that same message as the stream moves from upcoming to live to ended.
type Announcer struct {
	m       Messenger
	channel func() string // where new announcements go, asked each time
	mem     Memory

	// The roles to ping, asked each time; "" means no ping.
	upcomingRole, liveRole func() string

	// videoRole is pinged for a new video or Short ("" means no ping).
	videoRole func() string

	// Set for the community's channel (see WithCommunity): its messages name the channel.
	community string

	mu sync.Mutex // Discord runs each command in its own goroutine; one Update at a time
}

func NewAnnouncer(m Messenger, channelID string, mem Memory) *Announcer {
	return &Announcer{
		m: m, channel: constant(channelID), mem: mem,
		upcomingRole: constant(""), liveRole: constant(""), videoRole: constant(""),
	}
}

func constant(s string) func() string { return func() string { return s } }

// WithChannel makes the announcer ask where to post each time (so the channel can be changed while the bot runs) instead of using the one it was made with.
func (a *Announcer) WithChannel(channel func() string) *Announcer {
	a.channel = channel
	return a
}

// WithPingFuncs sets the roles to ping: upcomingRole when a new waiting room appears, liveRole when a stream goes live. They are asked each time, so they can change while the bot runs, and an empty answer switches that ping off.
func (a *Announcer) WithPingFuncs(upcomingRole, liveRole func() string) *Announcer {
	a.upcomingRole, a.liveRole = upcomingRole, liveRole
	return a
}

// WithVideoRole sets the role pinged for a new video or Short, asked each time.
func (a *Announcer) WithVideoRole(role func() string) *Announcer {
	a.videoRole = role
	return a
}

// WithCommunity makes this announcer the one for the community's channel: its messages carry the channel's name. Give it a Memory of its own, not the one her streams use: Waiting must only ever list the waiting rooms of the channel that its poller looks at.
func (a *Announcer) WithCommunity(name string) *Announcer {
	a.community = name
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
	defer a.mu.Unlock() // the lock is held during the Discord calls: fine at this size

	if s.IsUpload() {
		return a.upload(s)
	}
	text := a.textFor(s)
	prev, seen := a.mem.Get(s.VideoID)

	switch {
	case !seen && s.State == Ended:
		return nil // first seen when already over: nothing worth announcing
	case !seen:
		return a.post(s, text)
	case s.State < prev.State:
		return nil // stale information: streams only move forward
	case text == prev.Text:
		return nil // nothing changed, so do not touch Discord
	default:
		return a.edit(s, prev, text)
	}
}

// post announces a stream for the first time and pings for the state it is in.
func (a *Announcer) post(s Stream, text string) error {
	ch := a.channel()
	id, err := a.m.Send(ch, text)
	if err != nil {
		return err // not remembered, so the next Update tries again
	}
	if err := a.remember(s.VideoID, s.State, ch, id, text); err != nil {
		return err
	}
	return a.ping(s, ch)
}

// edit brings the message of a stream that is already announced up to date.
func (a *Announcer) edit(s Stream, prev Announced, text string) error {
	ch := a.postedIn(prev)
	id := prev.MessageID
	if err := a.m.Edit(ch, id, text); err != nil {
		if !errors.Is(err, ErrGone) {
			return err
		}
		// Somebody deleted the announcement. A stream that is over does not need a new one; a waiting room or a live stream does, in the channel announcements go to now.
		if s.State != Ended {
			ch = a.channel()
			fresh, err := a.m.Send(ch, text)
			if err != nil {
				return err // the old record stays, so the next Update tries again
			}
			id = fresh
		}
	}
	if err := a.remember(s.VideoID, s.State, ch, id, text); err != nil {
		return err
	}
	if s.State != prev.State && s.State != Ended {
		return a.ping(s, ch)
	}
	return nil
}

// upload announces an ordinary upload once. It is never edited afterwards, and one that is already announced is left alone.
func (a *Announcer) upload(s Stream) error {
	if _, seen := a.mem.Get(s.VideoID); seen {
		return nil
	}
	text := a.uploadText(s)
	ch := a.channel()
	id, err := a.m.Send(ch, text)
	if err != nil {
		return err // not remembered, so the next Update tries again
	}
	if err := a.remember(s.VideoID, Ended, ch, id, text); err != nil {
		return err
	}
	role := a.videoRole()
	if role == "" {
		return nil
	}
	if err := a.m.Ping(ch, a.uploadPing(s, role), role); err != nil {
		return fmt.Errorf("announced %s but could not ping: %w", s.VideoID, err)
	}
	return nil
}

// Waiting lists the streams of her own channel that are announced as waiting rooms and not yet cancelled: the ones a vanished waiting room could be among.
// Collabs on other channels are left out, because they are not on her channel and so cannot be told apart from vanished ones by looking at it.
func (a *Announcer) Waiting() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.mem.(Lister)
	if !ok {
		return nil
	}
	var out []string
	for _, id := range l.IDs() {
		if rec, ok := a.mem.Get(id); ok && isWaitingRoom(rec) {
			out = append(out, id)
		}
	}
	return out
}

// Cancel turns the announcement of a waiting room that has vanished from YouTube into a "cancelled" note. It does nothing for a stream that is not an announced waiting room, and nothing twice. If the room comes back, the next Update edits the message back to the normal waiting-room text.
func (a *Announcer) Cancel(videoID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	prev, ok := a.mem.Get(videoID)
	if !ok || !isWaitingRoom(prev) {
		return nil
	}
	text := msg.WaitingCancelled(prev.Text)
	ch := a.postedIn(prev)
	if err := a.m.Edit(ch, prev.MessageID, text); err != nil && !errors.Is(err, ErrGone) {
		return err
	}
	return a.remember(videoID, Upcoming, ch, prev.MessageID, text)
}

// isWaitingRoom says the record is a waiting room on her own channel that has not been cancelled. It goes by the first words of the message, which the collab texts do not share.
func isWaitingRoom(rec Announced) bool {
	return rec.State == Upcoming && strings.HasPrefix(rec.Text, msg.StreamUpcomingHead)
}

// postedIn is the channel a record was posted in, or the current announcement channel for records from before that was kept.
func (a *Announcer) postedIn(rec Announced) string {
	if rec.ChannelID != "" {
		return rec.ChannelID
	}
	return a.channel()
}

// ping mentions a role for the state the stream has just entered. It runs after the announcement is saved, so a failed ping is never retried: better one missed ping than the same ping twice.
func (a *Announcer) ping(s Stream, channelID string) error {
	var role string
	switch s.State {
	case Upcoming:
		role = a.upcomingRole()
	case Live:
		role = a.liveRole()
	}
	if role == "" {
		return nil
	}
	if err := a.m.Ping(channelID, a.pingText(s, role), role); err != nil {
		return fmt.Errorf("announced %s but could not ping: %w", s.VideoID, err)
	}
	return nil
}

// remember records what is now on Discord. If saving fails the message is still posted, so the error says so.
func (a *Announcer) remember(videoID string, state State, channelID, messageID, text string) error {
	rec := Announced{MessageID: messageID, State: state, Text: text, ChannelID: channelID}
	if err := a.mem.Put(videoID, rec); err != nil {
		return fmt.Errorf("announced %s but could not save it: %w", videoID, err)
	}
	return nil
}

// The wording. Three voices share one announcer type: her own channel, a collab on somebody else's channel (the stream names it), and the community's channel (the announcer names it).

// textFor is the announcement message of a stream in the state it is in.
func (a *Announcer) textFor(s Stream) string {
	premiere := s.Kind == KindPremiere
	switch {
	case a.community != "":
		switch s.State {
		case Live:
			return msg.CommunityLive(a.community, s.Title, s.URL(), premiere)
		case Ended:
			return msg.CommunityEnded(a.community, s.Title, s.URL(), premiere)
		default:
			return msg.CommunityUpcoming(a.community, s.Title, s.URL(), s.StartAt, premiere)
		}
	case s.Channel != "":
		switch s.State {
		case Live:
			return msg.CollabLive(s.Title, s.Channel, s.URL(), premiere)
		case Ended:
			return msg.CollabEnded(s.Title, s.Channel, s.URL(), premiere)
		default:
			return msg.CollabUpcoming(s.Title, s.Channel, s.URL(), s.StartAt, premiere)
		}
	default:
		switch s.State {
		case Live:
			return msg.StreamLive(s.Title, s.URL())
		case Ended:
			return msg.StreamEnded(s.Title, s.URL())
		default:
			return msg.StreamUpcoming(s.Title, s.URL(), s.StartAt)
		}
	}
}

// pingText is the message that mentions role. Only waiting rooms and live streams are pinged, so the state is one of those two.
func (a *Announcer) pingText(s Stream, role string) string {
	premiere, live := s.Kind == KindPremiere, s.State == Live
	switch {
	case a.community != "" && live:
		return msg.PingCommunityLive(role, a.community, s.Title, s.URL(), premiere)
	case a.community != "":
		return msg.PingCommunityUpcoming(role, a.community, s.Title, s.URL(), s.StartAt, premiere)
	case s.Channel != "" && live:
		return msg.PingCollabLive(role, s.Title, s.Channel, s.URL(), premiere)
	case s.Channel != "":
		return msg.PingCollabUpcoming(role, s.Title, s.Channel, s.URL(), s.StartAt, premiere)
	case live:
		return msg.PingLive(role, s.Title, s.URL())
	default:
		return msg.PingUpcoming(role, s.Title, s.URL(), s.StartAt)
	}
}

// uploadText and uploadPing word the announcement of a new video or Short.
func (a *Announcer) uploadText(s Stream) string {
	short := s.Kind == KindShort
	if a.community != "" {
		return msg.CommunityVideo(a.community, s.Title, s.URL(), short)
	}
	return msg.VideoNew(s.Title, s.URL(), short)
}

func (a *Announcer) uploadPing(s Stream, role string) string {
	short := s.Kind == KindShort
	if a.community != "" {
		return msg.PingCommunityVideo(role, a.community, s.Title, s.URL(), short)
	}
	return msg.PingVideo(role, s.Title, s.URL(), short)
}
