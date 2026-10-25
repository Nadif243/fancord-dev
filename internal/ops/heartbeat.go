// Package ops holds the bot's self-monitoring.
package ops

import (
	"context"
	"log/slog"
	"time"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Messenger is the little part of Discord the heartbeat needs. Keeping it an
// interface lets the tests run without Discord.
type Messenger interface {
	Send(channelID, content string) (messageID string, err error)
	Edit(channelID, messageID, content string) error
}

// Heartbeat keeps ONE message in the ops channel up to date instead of
// posting a new one every minute. Because the message shows "last beat <time>"
// as a live relative time, a stale heartbeat is obvious at a glance: if it
// says "last beat 20 minutes ago", the bot is down. The same
// edit-one-message pattern is how stream announcements will change from
// upcoming to live to ended.
type Heartbeat struct {
	m         Messenger
	channelID string
	version   string
	every     time.Duration
	log       *slog.Logger
	now       func() time.Time
}

func NewHeartbeat(m Messenger, channelID, version string, every time.Duration, log *slog.Logger) *Heartbeat {
	return &Heartbeat{m: m, channelID: channelID, version: version, every: every, log: log, now: time.Now}
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
	id, err := h.m.Send(h.channelID, content)
	if err != nil {
		h.log.Error("heartbeat: could not post", "err", err)
		return ""
	}
	return id
}

// update edits the heartbeat message. If it cannot (someone deleted it, or
// the first post failed) it posts a fresh one and returns the new ID.
func (h *Heartbeat) update(id, content string) string {
	if id != "" {
		err := h.m.Edit(h.channelID, id, content)
		if err == nil {
			return id
		}
		h.log.Warn("heartbeat: edit failed, posting a new message", "err", err)
	}
	return h.post(content)
}
