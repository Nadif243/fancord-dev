package discord

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// handler runs one slash command. Returning an error only logs it; the user already got whatever the handler managed to reply.
type handler func(s *discordgo.Session, i *discordgo.InteractionCreate) error

// Router owns the bot's slash commands: their definitions (what Discord shows) and their handlers (what runs). To add a command, add one entry in NewRouter.
type Router struct {
	log      *slog.Logger
	defs     []*discordgo.ApplicationCommand
	handlers map[string]handler

	announcer    *broadcast.Announcer
	devChannelID string // the only channel where dev-only commands work
}

func NewRouter(log *slog.Logger, announcer *broadcast.Announcer, devChannelID string) *Router {
	r := &Router{
		log:          log,
		handlers:     map[string]handler{},
		announcer:    announcer,
		devChannelID: devChannelID,
	}

	r.add(&discordgo.ApplicationCommand{Name: "ping", Description: msg.PingDescription}, handlePing)

	// /devstream pretends a stream changed state, so the announcer can be watched in Discord without a real stream. r.handleDevStream is a "method value": a function that remembers which Router it belongs to.
	r.add(&discordgo.ApplicationCommand{
		Name:        "devstream",
		Description: msg.DevStreamDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "state",
			Description: msg.DevStreamStateDescription,
			Required:    true,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: "upcoming", Value: "upcoming"},
				{Name: "live", Value: "live"},
				{Name: "ended", Value: "ended"},
			},
		}},
	}, r.handleDevStream)

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

// handleDevStream feeds one fake stream to the announcer in the chosen state.
func (r *Router) handleDevStream(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if i.ChannelID != r.devChannelID {
		return reply(s, i, msg.DevOnly)
	}

	state := i.ApplicationCommandData().Options[0].StringValue()
	st := broadcast.Stream{
		VideoID: "DEVSTREAM01", // not a real video, so Discord shows no preview
		Title:   "Stream percobaan (dev)",
		StartAt: time.Now().Add(30 * time.Minute),
	}
	switch state {
	case "upcoming":
		st.State = broadcast.Upcoming
	case "live":
		st.State = broadcast.Live
	case "ended":
		st.State = broadcast.Ended
	default:
		return reply(s, i, msg.UnknownCommand)
	}

	if err := r.announcer.Update(st); err != nil {
		_ = reply(s, i, msg.CommandFailed)
		return fmt.Errorf("announce dev stream: %w", err)
	}
	return reply(s, i, msg.DevStreamSet(state))
}
