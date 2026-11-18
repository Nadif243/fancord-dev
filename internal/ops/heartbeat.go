// Package ops holds the bot's self-monitoring.
package ops

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Messenger is the little part of Discord the heartbeat needs. Keeping it an interface lets the tests run without Discord.
type Messenger interface {
	Send(channelID, content string) (messageID string, err error)

	// Edit changes a message. When the message (or its channel) no longer exists the error wraps broadcast.ErrGone; the discord messenger does that.
	Edit(channelID, messageID, content string) error
}

// Heartbeat keeps ONE message in the ops channel up to date instead of posting a new one every minute. Because the message shows "last beat <time>" as a live relative time, a stale heartbeat is obvious at a glance: if it says "last beat 20 minutes ago", the bot is down. Stream announcements use the same edit-one-message pattern.
type Heartbeat struct {
	m       Messenger
	channel func() string // where the message lives, asked each time
	version string
	every   time.Duration
	log     *slog.Logger
	now     func() time.Time
}

func NewHeartbeat(m Messenger, channelID, version string, every time.Duration, log *slog.Logger) *Heartbeat {
	return &Heartbeat{
		m: m, channel: func() string { return channelID },
		version: version, every: every, log: log, now: time.Now,
	}
}

// WithChannel makes the heartbeat ask where to post each time, instead of using the channel it was made with. After a change the next beat cannot edit the old message in the new channel, so it posts a fresh one there (update does that when an edit fails because the message is gone).
func (h *Heartbeat) WithChannel(channel func() string) *Heartbeat {
	h.channel = channel
	return h
}

// Run blocks until ctx is cancelled, then marks the message Offline.
func (h *Heartbeat) Run(ctx context.Context) {
	started := h.now()
	id := h.post(msg.HeartbeatOnline(h.version, started, started))

	ticker := time.NewTicker(h.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.update(id, msg.HeartbeatOffline(h.version, h.now()))
			return
		case <-ticker.C:
			id = h.update(id, msg.HeartbeatOnline(h.version, started, h.now()))
		}
	}
}

func (h *Heartbeat) post(content string) string {
	id, err := h.m.Send(h.channel(), content)
	if err != nil {
		h.log.Error("heartbeat: could not post", "err", err)
		return ""
	}
	return id
}

// update edits the heartbeat message and returns its ID. A new message is posted only when there is none (the first post failed) or it was deleted. Any other failed edit (a network hiccup, a rate limit) keeps the same message and is tried again at the next beat, so one bad minute never leaves two heartbeats.
func (h *Heartbeat) update(id, content string) string {
	if id == "" {
		return h.post(content)
	}
	err := h.m.Edit(h.channel(), id, content)
	switch {
	case err == nil:
		return id
	case errors.Is(err, broadcast.ErrGone):
		h.log.Warn("heartbeat: the message is gone, posting a new one", "err", err)
		return h.post(content)
	default:
		h.log.Warn("heartbeat: edit failed, will try again", "err", err)
		return id
	}
}
