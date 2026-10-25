package discord

import (
	"fmt"
	"log/slog"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

// handler runs one slash command. Returning an error only logs it; the user already got whatever the handler managed to reply.
type handler func(s *discordgo.Session, i *discordgo.InteractionCreate) error

// Router owns the bot's slash commands: their definitions (what Discord shows) and their handlers (what runs). To add a command, add one entry in
// NewRouter.
type Router struct {
	log      *slog.Logger
	defs     []*discordgo.ApplicationCommand
	handlers map[string]handler
}

func NewRouter(log *slog.Logger) *Router {
	r := &Router{log: log, handlers: map[string]handler{}}
	r.add(&discordgo.ApplicationCommand{Name: "ping", Description: msg.PingDescription}, handlePing)
	return r
}

func (r *Router) add(def *discordgo.ApplicationCommand, h handler) {
	r.defs = append(r.defs, def)
	r.handlers[def.Name] = h
}

// Register replaces ALL of this app's commands in one guild with ours. Guild commands appear instantly, unlike global ones, which can take up to an hour.
func (r *Router) Register(s *discordgo.Session, appID, guildID string) error {
	if _, err := s.ApplicationCommandBulkOverwrite(appID, guildID, r.defs); err != nil {
		return fmt.Errorf("register slash commands: %w", err)
	}
	return nil
}

// OnInteraction is the handler to pass to Session.AddHandler.
func (r *Router) OnInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	name := i.ApplicationCommandData().Name
	log := r.log.With("command", name)

	// A bug in one command must not take the whole bot down.
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("command panicked", "panic", rec)
			_ = reply(s, i, msg.CommandFailed)
		}
	}()

	h, ok := r.handlers[name]
	if !ok {
		log.Warn("unknown command")
		_ = reply(s, i, msg.UnknownCommand)
		return
	}
	if err := h(s, i); err != nil {
		log.Error("command failed", "err", err)
	}
}

func reply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

func handlePing(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return reply(s, i, msg.Pong(s.HeartbeatLatency()))
}
