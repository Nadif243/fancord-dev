package discord

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// handler runs one slash command. Returning an error only logs it; the user already got whatever the handler managed to reply.
type handler func(s *discordgo.Session, i *discordgo.InteractionCreate) error

// StreamSource is anything that can list a channel's recent streams. *youtube.Client satisfies it without knowing this interface exists.
type StreamSource interface {
	RecentStreams(ctx context.Context, channelID string, pages int) (broadcast.Scan, error)
}

// RecapPreviewer posts the recap of a past week into a channel.
// *recap.Service satisfies it.
type RecapPreviewer interface {
	Preview(channelID string, weeksAgo int) error
}

// Deps is everything the commands need from the rest of the bot, gathered in one struct so NewRouter does not grow a new parameter for every feature.
type Deps struct {
	DevAnnouncer     *broadcast.Announcer // for /devstream only: it posts fake streams and forgets them
	DevChannelID     string               // the only channel where dev-only commands work
	Source           StreamSource         // nil when no YouTube key is configured
	YouTubeChannelID string               // her channel, the default for /devcheck
	Recap            RecapPreviewer       // nil when the recap is off
}

// Router owns the bot's slash commands: their definitions (what Discord shows) and their handlers (what runs). To add a command, add one entry in NewRouter.
type Router struct {
	log      *slog.Logger
	defs     []*discordgo.ApplicationCommand
	handlers map[string]handler
	deps     Deps
}

func NewRouter(log *slog.Logger, deps Deps) *Router {
	r := &Router{log: log, handlers: map[string]handler{}, deps: deps}

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

	// /devcheck reads real YouTube data and only shows it; it announces nothing.
	r.add(&discordgo.ApplicationCommand{
		Name:        "devcheck",
		Description: msg.DevCheckDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "channel",
			Description: msg.DevCheckChannelDescription,
			Required:    false,
		}, {
			Type:        discordgo.ApplicationCommandOptionInteger,
			Name:        "pages",
			Description: msg.DevCheckPagesDescription,
			Required:    false,
		}},
	}, r.handleDevCheck)

	// /devrecap posts a past week's recap here, so the layout can be judged without waiting for Sunday.
	r.add(&discordgo.ApplicationCommand{
		Name:        "devrecap",
		Description: msg.DevRecapDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionInteger,
			Name:        "weeks",
			Description: msg.DevRecapWeeksDescription,
			Required:    false,
		}},
	}, r.handleDevRecap)

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
	if i.ChannelID != r.deps.DevChannelID {
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

	if err := r.deps.DevAnnouncer.Update(st); err != nil {
		_ = reply(s, i, msg.CommandFailed)
		return fmt.Errorf("announce dev stream: %w", err)
	}
	return reply(s, i, msg.DevStreamSet(state))
}

// handleDevCheck fetches her recent streams from YouTube and lists them in the reply. Pass another channel ID to try the same on any channel.
func (r *Router) handleDevCheck(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if i.ChannelID != r.deps.DevChannelID {
		return reply(s, i, msg.DevOnly)
	}
	if r.deps.Source == nil {
		return reply(s, i, msg.DevCheckNoKey)
	}

	channelID := r.deps.YouTubeChannelID
	pages := 1
	for _, opt := range i.ApplicationCommandData().Options {
		switch opt.Name {
		case "channel":
			channelID = strings.TrimSpace(opt.StringValue())
		case "pages":
			pages = int(opt.IntValue()) // the youtube package keeps it within 1..MaxPages
		}
	}
	if channelID == "" {
		return reply(s, i, msg.DevCheckNoChannel)
	}

	// Discord wants a first answer within 3 seconds and YouTube can be slower, so answer "thinking..." now and replace it with the real text afterwards.
	if err := deferReply(s, i); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	scan, err := r.deps.Source.RecentStreams(ctx, channelID, pages)
	if err != nil {
		_ = editReply(s, i, msg.DevCheckFailed(err.Error()))
		return fmt.Errorf("check youtube channel %s: %w", channelID, err)
	}
	return editReply(s, i, devCheckText(scan))
}

// handleDevRecap posts the recap of the chosen week into this channel.
func (r *Router) handleDevRecap(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if i.ChannelID != r.deps.DevChannelID {
		return reply(s, i, msg.DevOnly)
	}
	if r.deps.Recap == nil {
		return reply(s, i, msg.DevRecapOff)
	}

	weeks := 0
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "weeks" {
			weeks = int(opt.IntValue())
		}
	}
	weeks = max(0, min(weeks, 52)) // the history is far shorter than a year anyway

	// The preview is posted as ordinary messages (the same code the Sunday post uses), so the reply only has to say it went out.
	if err := r.deps.Recap.Preview(i.ChannelID, weeks); err != nil {
		_ = reply(s, i, msg.CommandFailed)
		return fmt.Errorf("preview recap: %w", err)
	}
	return reply(s, i, msg.DevRecapSent)
}

// deferReply shows "the bot is thinking..." and gives us 15 minutes to answer.
func deferReply(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// editReply replaces the "thinking..." message with the real answer.
func editReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
	return err
}

// A Discord message holds at most 2000 characters, so only the first lines of the list are shown.
const devCheckMaxLines = 10

func devCheckText(scan broadcast.Scan) string {
	if len(scan.Streams) == 0 {
		return withNotes(msg.DevCheckNone(scan.Checked), scan.Notes)
	}

	// Count for the header, and put the interesting ones first: live, then upcoming, then premieres, then ended streams. The sort is stable, so YouTube's order (newest first) is kept within each group.
	var upcoming, live, ended, premieres int
	for _, st := range scan.Streams {
		switch {
		case st.Kind == broadcast.KindPremiere:
			premieres++
		case st.State == broadcast.Upcoming:
			upcoming++
		case st.State == broadcast.Live:
			live++
		default:
			ended++
		}
	}
	streams := slices.Clone(scan.Streams) // do not reorder the caller's slice
	slices.SortStableFunc(streams, func(a, b broadcast.Stream) int {
		return showRank(a) - showRank(b)
	})

	var b strings.Builder
	b.WriteString(msg.DevCheckSummary(scan.Checked, upcoming, live, ended, premieres))
	for n, st := range streams {
		if n == devCheckMaxLines {
			b.WriteString("\n" + msg.DevCheckMore(len(streams)-n))
			break
		}
		b.WriteString("\n" + msg.DevCheckLine(icon(st), st.Title, st.StartAt))
	}
	return withNotes(b.String(), scan.Notes)
}

func withNotes(text string, notes []string) string {
	for _, note := range notes {
		text += "\n" + msg.DevCheckNote(note)
	}
	return text
}

// showRank is the order /devcheck lists things in.
func showRank(st broadcast.Stream) int {
	switch {
	case st.Kind == broadcast.KindPremiere:
		return 2
	case st.State == broadcast.Live:
		return 0
	case st.State == broadcast.Upcoming:
		return 1
	default:
		return 3
	}
}

func icon(st broadcast.Stream) string {
	switch {
	case st.Kind == broadcast.KindPremiere:
		return "🎬"
	case st.State == broadcast.Upcoming:
		return "⏰"
	case st.State == broadcast.Live:
		return "🔴"
	default:
		return "⚫"
	}
}
