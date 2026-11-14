package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/collabs"
	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/outside"
	"github.com/Nadif243/fancord-bot/internal/partners"
	"github.com/Nadif243/fancord-bot/internal/rodok"
	"github.com/Nadif243/fancord-bot/internal/rolepanel"
	"github.com/Nadif243/fancord-bot/internal/which"
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

// ReminderPreviewer posts this week's collab reminder into a channel and says how many streams it listed and how many the week has. *recap.Service satisfies it.
type ReminderPreviewer interface {
	PreviewReminder(channelID string) (pending, total int, err error)
}

// CollabTagger lets people say who was in a stream. *collabs.Service satisfies it.
type CollabTagger interface {
	Recent(query string, n int) []collabs.StreamRef
	Suggest(typed string, n int) []partners.Suggestion
	Change(videoID, add, remove string) (collabs.Outcome, error)
}

// NicknameAsker takes the answers to "who is meant?" questions.
// *which.Service satisfies it.
type NicknameAsker interface {
	Answer(key, choice, by string) (which.Result, error)
}

// CollabReviewer takes the links people send and the reviewers' decisions.
// *outside.Service satisfies it.
type CollabReviewer interface {
	Submit(ctx context.Context, who, name, text string, allowX bool) outside.Reply
	Review(action, videoID, by string) (outside.Result, error)
}

// Permission bits of a Discord member (see Discord's permissions table).
// Kept here as plain numbers so this file does not depend on the library's constant names.
const (
	permAdministrator int64 = 1 << 3
	permManageGuild   int64 = 1 << 5
)

// Deps is everything the commands need from the rest of the bot, gathered in one struct so NewRouter does not grow a new parameter for every feature.
type Deps struct {
	DevAnnouncer     *broadcast.Announcer // for /devstream only: it posts fake streams and forgets them
	DevChannelID     string               // the only channel where dev-only commands work
	Source           StreamSource         // nil when no YouTube key is configured
	YouTubeChannelID string               // her channel, the default for /devcheck
	Recap            RecapPreviewer       // nil when the recap is off
	Reminder         ReminderPreviewer    // nil when the reminder is off
	Collab           CollabTagger         // nil when the recap is off
	Outside          CollabReviewer       // nil when collab review is off
	Which            NicknameAsker        // nil when there are no nickname questions
	Rodok            Rodoker              // nil when /rodok is not set up
	Roles            RolePanel            // nil when the role panel is off

	// LinkChannelID is the fan channel where links are read from messages.
	LinkChannelID string

	// These three are asked each time when set (so /setup can change them while the bot runs) and replace the fixed IDs of the same name.
	DevChannel  func() string
	LinkChannel func() string
	ReviewRole  func() string

	// Settings is what /setup changes. Nil: /setup says it is not available.
	Settings SettingsAdmin

	// ListeningForMessages says whether the bot asked Discord for the Message Content intent when it connected. A fan channel set later by /setup needs one restart before it works if this is false.
	ListeningForMessages bool

	// ReviewRoleID is the role that may press Confirm and Ignore. When it is empty, members with Manage Server or Administrator may instead.
	ReviewRoleID string
}

// devChannel, linkChannel and reviewRole are what the deps say right now.
func (r *Router) devChannel() string {
	if r.deps.DevChannel != nil {
		return r.deps.DevChannel()
	}
	return r.deps.DevChannelID
}

// inDevChannel says whether the command was typed in the staff channel, the only place the dev commands and /collab work.
func (r *Router) inDevChannel(i *discordgo.InteractionCreate) bool {
	dev := r.devChannel()
	return dev != "" && i.ChannelID == dev
}

// mayReview says whether this member may press the review buttons right now.
func (r *Router) mayReview(m *discordgo.Member) bool {
	return canReview(m, r.reviewRole())
}

func (r *Router) linkChannel() string {
	if r.deps.LinkChannel != nil {
		return r.deps.LinkChannel()
	}
	return r.deps.LinkChannelID
}

func (r *Router) reviewRole() string {
	if r.deps.ReviewRole != nil {
		return r.deps.ReviewRole()
	}
	return r.deps.ReviewRoleID
}

// Rodoker counts and limits /rodok. *rodok.Service satisfies it.
type Rodoker interface {
	Do(actor, target string) (rodok.Result, error)
}

// Router owns the bot's slash commands: their definitions (what Discord shows) and their handlers (what runs). To add a command, add one entry in NewRouter.
type Router struct {
	log      *slog.Logger
	defs     []*discordgo.ApplicationCommand
	handlers map[string]handler
	// suggest answers the "type to search" lists of a command's options, by command name. It returns what to offer for the option being typed.
	suggest map[string]func(*discordgo.InteractionCreate) []*discordgo.ApplicationCommandOptionChoice
	deps    Deps
}

func NewRouter(log *slog.Logger, deps Deps) *Router {
	r := &Router{
		log:      log,
		handlers: map[string]handler{},
		suggest:  map[string]func(*discordgo.InteractionCreate) []*discordgo.ApplicationCommandOptionChoice{},
		deps:     deps,
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

	// /devremind posts this week's collab reminder here, without waiting for Saturday.
	r.add(&discordgo.ApplicationCommand{
		Name:        "devremind",
		Description: msg.DevRemindDescription,
	}, r.handleDevRemind)

	// /collab says who was in a stream with her. Her titles often do not say, so people who watched (or who ask on Saturday) tag it by hand. All three options offer a list while typing; see collabChoices.
	r.add(&discordgo.ApplicationCommand{
		Name:        "collab",
		Description: msg.CollabDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "stream",
			Description:  msg.CollabStreamDescription,
			Required:     true,
			Autocomplete: true,
		}, {
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "add",
			Description:  msg.CollabAddDescription,
			Autocomplete: true,
		}, {
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "remove",
			Description:  msg.CollabRemoveDescription,
			Autocomplete: true,
		}},
	}, r.handleCollab)
	r.suggest["collab"] = r.collabChoices

	// /collablink is for everyone: fans send the YouTube link of a collab on another channel; reviewers then confirm it with the buttons.
	r.add(&discordgo.ApplicationCommand{
		Name:        "collablink",
		Description: msg.CollabLinkDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "link",
			Description: msg.CollabLinkOptionDescription,
			Required:    true,
		}},
	}, r.handleCollabLink)

	// /setup is for people with Manage Server; it changes channels and roles while the bot runs.
	r.addSetup()

	// /rolepanel sets up the role-choosing panel (Manage Server only).
	if deps.Roles != nil {
		r.addRolePanel()
	}

	// /rodok is for everyone and works in any channel.
	r.add(&discordgo.ApplicationCommand{
		Name:        "rodok",
		Description: msg.RodokDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionUser,
			Name:        "user",
			Description: msg.RodokOptionDescription,
			Required:    true,
		}},
	}, r.handleRodok)

	// /rodokboard shows who rodoks the most, and who gets rodok-ed the most.
	r.add(&discordgo.ApplicationCommand{
		Name:        "rodokboard",
		Description: msg.RodokBoardDescription,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "peringkat",
			Description: msg.RodokBoardOptionDescription,
			Required:    true,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: msg.RodokBoardChoiceDoers, Value: boardDoers},
				{Name: msg.RodokBoardChoiceTargets, Value: boardTargets},
			},
		}},
	}, r.handleRodokBoard)

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
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		r.onCommand(s, i)
	case discordgo.InteractionApplicationCommandAutocomplete:
		r.onAutocomplete(s, i)
	case discordgo.InteractionMessageComponent:
		r.onComponent(s, i)
	}
}

// onAutocomplete answers "what can I offer while this option is being typed?".
// Discord wants an answer within 3 seconds, so this only looks at memory.
// Anything that goes wrong simply offers nothing: it must never break typing.
func (r *Router) onAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	name := i.ApplicationCommandData().Name
	log := r.log.With("command", name, "autocomplete", true)

	choices := []*discordgo.ApplicationCommandOptionChoice{} // never nil: Discord rejects null
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("autocomplete panicked", "panic", rec)
			}
		}()
		if f, ok := r.suggest[name]; ok {
			choices = append(choices, f(i)...)
		}
	}()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
	if err != nil {
		log.Error("could not answer autocomplete", "err", err)
	}
}

func (r *Router) onCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		// Whatever is in the text (a title, a typed name), it can never ping anyone.
		Data: &discordgo.InteractionResponseData{Content: content, AllowedMentions: nobody()},
	})
}

// replyPrivate answers so that only the person who pressed or typed sees it.
func replyPrivate(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, AllowedMentions: nobody(), Flags: discordgo.MessageFlagsEphemeral},
	})
}

// canReview says whether this member may press the review buttons: with a reviewer role configured, only people who have it; without one, only members with Manage Server or Administrator (so the buttons are never open to everybody).
func canReview(m *discordgo.Member, roleID string) bool {
	if m == nil {
		return false
	}
	if roleID != "" {
		return slices.Contains(m.Roles, roleID)
	}
	return m.Permissions&(permAdministrator|permManageGuild) != 0
}

// onComponent handles a press on a button under a review message.
func (r *Router) onComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	if key, choice, ok := which.ParseCustomID(customID); ok {
		r.onWhich(s, i, key, choice)
		return
	}
	if roleID, ok := rolepanel.ParseCustomID(customID); ok {
		r.onRole(s, i, roleID)
		return
	}
	action, videoID, ok := outside.ParseCustomID(customID)
	if !ok {
		return // not one of ours
	}
	log := r.log.With("button", action, "video", videoID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("button panicked", "panic", rec)
			_ = replyPrivate(s, i, msg.CommandFailed)
		}
	}()

	switch {
	case r.deps.Outside == nil:
		_ = replyPrivate(s, i, msg.CollabReviewOff)
		return
	case !r.mayReview(i.Member):
		_ = replyPrivate(s, i, msg.CollabReviewDenied)
		return
	}

	res, err := r.deps.Outside.Review(action, videoID, userName(i))
	if errors.Is(err, outside.ErrUnknown) {
		_ = replyPrivate(s, i, msg.CollabReviewOff)
		return
	}
	if err != nil {
		log.Error("could not save the decision (it holds until restart)", "err", err)
	}
	// Edit the review message itself: new text, and an EMPTY list of components
	// (not nil) so the buttons disappear.
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:         res.Text,
			Components:      []discordgo.MessageComponent{},
			AllowedMentions: nobody(),
		},
	})
	if err != nil {
		log.Error("could not update the review message", "err", err)
	}
}

// onWhich handles a press on a button under a "who is meant?" question. The people who may press are the same as for the review buttons.
func (r *Router) onWhich(s *discordgo.Session, i *discordgo.InteractionCreate, key, choice string) {
	log := r.log.With("button", "which", "question", key)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("button panicked", "panic", rec)
			_ = replyPrivate(s, i, msg.CommandFailed)
		}
	}()
	switch {
	case r.deps.Which == nil:
		_ = replyPrivate(s, i, msg.CollabReviewOff)
		return
	case !r.mayReview(i.Member):
		_ = replyPrivate(s, i, msg.CollabReviewDenied)
		return
	}
	res, err := r.deps.Which.Answer(key, choice, userName(i))
	if errors.Is(err, which.ErrUnknown) || errors.Is(err, which.ErrBadChoice) {
		_ = replyPrivate(s, i, msg.CollabReviewOff)
		return
	}
	if err != nil {
		log.Error("could not save the answer (it holds until restart)", "err", err)
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:         res.Text,
			Components:      []discordgo.MessageComponent{},
			AllowedMentions: nobody(),
		},
	})
	if err != nil {
		log.Error("could not update the question", "err", err)
	}
}

// handleCollabLink queues the YouTube links in what a fan typed. X links are not read here (that needs more than the three seconds a command has); the fan channel reads them.
func (r *Router) handleCollabLink(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if r.deps.Outside == nil {
		return replyPrivate(s, i, msg.CollabLinkOff)
	}
	var text string
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "link" {
			text = opt.StringValue()
		}
	}
	return replyPrivate(s, i, linkText(r.submit(false, userKey(i), userName(i), text)))
}

// handleRodok answers "A merodok B Nx". Answers are public but ping nobody (see reply). The playful refusals for self and bot are public too, like the normal answer; only the spam messages (cooldown, lockout) are private, so a spammer does not fill the channel.
func (r *Router) handleRodok(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if r.deps.Rodok == nil {
		return replyPrivate(s, i, msg.CollabLinkOff)
	}
	target, isBot := pickUser(i.ApplicationCommandData(), "user")
	text, public, err := rodokAnswer(r.deps.Rodok, userKey(i), target, isBot)
	if err != nil {
		// The count is right for this run but could not be saved.
		r.log.Error("could not save the rodok counts", "err", err)
	}
	if public {
		return reply(s, i, text)
	}
	return replyPrivate(s, i, text)
}

// The two values of /rodokboard's choice.
const (
	boardDoers   = "pelaku"
	boardTargets = "korban"
)

// boardSize is how many people the leaderboard lists.
const boardSize = 10

// boardColor is the colour of the embed's stripe (a warm red).
const boardColor = 0xE8505B

// handleRodokBoard shows the leaderboard as an embed. Like the normal answer it is public, and the mentions in it show names without pinging anyone.
func (r *Router) handleRodokBoard(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	board, ok := r.deps.Rodok.(rodok.Board)
	if r.deps.Rodok == nil || !ok {
		return replyPrivate(s, i, msg.RodokBoardOff)
	}
	embed := rodokBoardEmbed(board, boardIsDoers(i.ApplicationCommandData()), userKey(i))
	if embed == nil {
		return reply(s, i, msg.RodokBoardEmpty)
	}
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: nobody()},
	})
}

// boardIsDoers says whether the person picked "pelaku" (true) or "korban".
func boardIsDoers(data discordgo.ApplicationCommandInteractionData) bool {
	for _, opt := range data.Options {
		if opt.Name == "peringkat" {
			kind, _ := opt.Value.(string)
			return kind == boardDoers
		}
	}
	return false
}

// rodokBoardEmbed builds the leaderboard; nil when nobody has rodok-ed yet.
// viewer is whoever asked: when they are not on the list, their own place is shown under it.
func rodokBoardEmbed(b rodok.Board, doers bool, viewer string) *discordgo.MessageEmbed {
	role := rodok.Targets
	if doers {
		role = rodok.Doers
	}
	top := b.Top(role, boardSize)
	if len(top) == 0 {
		return nil
	}
	var lines []string
	onList := false
	for n, e := range top {
		lines = append(lines, msg.RodokBoardLine(n+1, e.ID, e.Total, e.People, doers))
		if e.ID == viewer {
			onList = true
		}
	}
	text := strings.Join(lines, "\n")
	if viewer != "" && !onList {
		if rank, e, ok := b.Rank(role, viewer); ok {
			text += "\n\n" + msg.RodokBoardYou(rank, e.Total)
		}
	}
	return &discordgo.MessageEmbed{
		Title:       msg.RodokBoardTitle(doers),
		Description: text,
		Color:       boardColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: msg.RodokBoardFooter(b.Total())},
	}
}

// rodokAnswer decides what to say. public is false only for the spam messages and the "not in a server" case. An error only means the count could not be saved; the text is still right.
func rodokAnswer(r Rodoker, actor, target string, targetIsBot bool) (text string, public bool, err error) {
	if actor == "" || target == "" { // a direct message: there is no server member
		return msg.RodokGuildOnly, false, nil
	}
	// These two are refused before the limiter, so they use up nothing.
	if target == actor {
		return msg.RodokSelf, true, nil
	}
	if targetIsBot {
		return msg.RodokBot, true, nil
	}

	res, err := r.Do(actor, target)
	switch res.Outcome {
	case rodok.Cooling:
		return msg.RodokCooling(res.Wait), false, err
	case rodok.Locked:
		return msg.RodokLocked(res.Wait), false, err
	}
	return msg.Rodok(actor, target, res.Count), true, err
}

// pickUser returns the ID of the user picked in the named option, and whether that user is a bot. Discord sends the ID as the option's value and the user's details in "resolved".
func pickUser(data discordgo.ApplicationCommandInteractionData, name string) (id string, isBot bool) {
	for _, opt := range data.Options {
		if opt.Name != name {
			continue
		}
		id, _ = opt.Value.(string)
		if data.Resolved != nil { // reading a field through a nil pointer would panic
			if u := data.Resolved.Users[id]; u != nil {
				isBot = u.Bot
			}
		}
	}
	return id, isBot
}

// submit hands a person's text to the review service. Only the fan channel reads X posts: a slash command has three seconds to answer, which an X lookup can exceed.
func (r *Router) submit(fromChannel bool, who, name, text string) outside.Reply {
	return r.deps.Outside.Submit(context.Background(), who, name, text, fromChannel)
}

// linkText is what a person is told about their submission.
func linkText(r outside.Reply) string {
	switch r.Outcome {
	case outside.Queued:
		return msg.CollabLinkThanks(r.New)
	case outside.Known:
		return msg.CollabLinkKnown
	case outside.Slow:
		return msg.CollabLinkSlow
	case outside.XNotHere:
		return msg.CollabLinkXNotHere
	case outside.XNoLink:
		return msg.CollabLinkXNoLink
	case outside.XCapReached:
		return msg.CollabLinkXCap
	case outside.XFailed:
		return msg.CollabLinkXFailed
	case outside.XNotFound:
		return msg.CollabLinkXNotFound
	}
	return msg.CollabLinkNone
}

// Emoji the bot puts under a message in the fan channel.
const (
	reactQueued = "✅"
	reactKnown  = "🔁"
	reactSlow   = "⏳"
)

// channelAnswer decides how the bot answers a message in the fan channel: a small reaction when that says enough, a written reply when the person needs to do something, and nothing for ordinary chat.
func channelAnswer(r outside.Reply) (reaction, reply string) {
	switch r.Outcome {
	case outside.Queued:
		return reactQueued, ""
	case outside.Known:
		return reactKnown, ""
	case outside.Slow:
		return reactSlow, ""
	case outside.NoLink:
		return "", ""
	}
	return "", linkText(r)
}

// fanMessage says whether a message is one the fan channel should read, and who sent it. Bots (this one included) and every other channel are skipped.
func (r *Router) fanMessage(m *discordgo.MessageCreate) (who, name string, ok bool) {
	if m == nil || m.Message == nil || r.linkChannel() == "" || m.ChannelID != r.linkChannel() || r.deps.Outside == nil {
		return "", "", false
	}
	if m.Author == nil || m.Author.Bot {
		return "", "", false
	}
	name = m.Author.Username
	if m.Member != nil && m.Member.Nick != "" {
		name = m.Member.Nick
	}
	return m.Author.ID, name, true
}

// OnMessage is the handler for messages in the fan channel. Pass it to Session.AddHandler only when the channel is set (it needs the Message Content intent).
func (r *Router) OnMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	who, name, ok := r.fanMessage(m)
	if !ok {
		return
	}
	log := r.log.With("message", m.ID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("fan channel handler panicked", "panic", rec)
		}
	}()
	reaction, text := channelAnswer(r.submit(true, who, name, m.Content))
	if reaction != "" {
		if err := s.MessageReactionAdd(m.ChannelID, m.ID, reaction); err != nil {
			log.Warn("could not react", "err", err)
		}
	}
	if text != "" {
		_, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
			Content:         text,
			Reference:       m.Reference(),
			AllowedMentions: nobody(),
		})
		if err != nil {
			log.Warn("could not reply", "err", err)
		}
	}
}

// userKey identifies who ran the command (their Discord ID).
func userKey(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	return ""
}

func handlePing(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return reply(s, i, msg.Pong(s.HeartbeatLatency()))
}

// handleDevStream feeds one fake stream to the announcer in the chosen state.
func (r *Router) handleDevStream(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if !r.inDevChannel(i) {
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
	if !r.inDevChannel(i) {
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
	if !r.inDevChannel(i) {
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

// handleDevRemind posts this week's collab reminder into this channel.
func (r *Router) handleDevRemind(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if !r.inDevChannel(i) {
		return reply(s, i, msg.DevOnly)
	}
	if r.deps.Reminder == nil {
		return reply(s, i, msg.DevRemindOff)
	}
	pending, total, err := r.deps.Reminder.PreviewReminder(i.ChannelID)
	if err != nil {
		_ = reply(s, i, msg.CommandFailed)
		return fmt.Errorf("preview reminder: %w", err)
	}
	if pending == 0 {
		return reply(s, i, msg.CollabReminderNone(total))
	}
	return reply(s, i, msg.DevRemindSent)
}

// maxChoices is the most options Discord lets an autocomplete list hold.
const maxChoices = 25

// collabChoices offers what fits the option being typed: streams for "stream", partners and teams for "add" and "remove". Only in the dev channel.
func (r *Router) collabChoices(i *discordgo.InteractionCreate) []*discordgo.ApplicationCommandOptionChoice {
	if r.deps.Collab == nil || !r.inDevChannel(i) {
		return nil
	}
	for _, opt := range i.ApplicationCommandData().Options {
		if !opt.Focused {
			continue
		}
		typed := opt.StringValue()
		switch opt.Name {
		case "stream":
			return streamChoices(r.deps.Collab.Recent(typed, maxChoices))
		case "add", "remove":
			prefix, term := splitTyped(typed)
			return nameChoices(prefix, r.deps.Collab.Suggest(term, maxChoices))
		}
	}
	return nil
}

// streamChoices turns streams into list entries: the label is what a person reads, the value (the video ID) is what the command receives.
func streamChoices(refs []collabs.StreamRef) []*discordgo.ApplicationCommandOptionChoice {
	var out []*discordgo.ApplicationCommandOptionChoice
	for _, ref := range refs {
		if len(out) == maxChoices {
			break
		}
		out = append(out, &discordgo.ApplicationCommandOptionChoice{
			Name:  msg.CollabStreamLabel(ref.Start, ref.Title, len(ref.Partners) > 0, ref.Checked),
			Value: ref.ID,
		})
	}
	return out
}

// splitTyped separates what was typed into the finished part and the name being typed now: "dina, ind" is "dina, " and "ind".
func splitTyped(typed string) (prefix, term string) {
	i := strings.LastIndex(typed, ",")
	if i < 0 {
		return "", strings.TrimSpace(typed)
	}
	return strings.TrimSpace(typed[:i]) + ", ", strings.TrimSpace(typed[i+1:])
}

// nameChoices offers partners and teams. Picking one replaces the whole text of the option, so every value starts with what was already typed.
func nameChoices(prefix string, suggestions []partners.Suggestion) []*discordgo.ApplicationCommandOptionChoice {
	var out []*discordgo.ApplicationCommandOptionChoice
	for _, sg := range suggestions {
		value := prefix + sg.Name
		if len([]rune(value)) > 100 { // Discord's limit for a value
			continue
		}
		label := []rune(prefix + msg.CollabNameLabel(sg.Name, sg.Members))
		if len(label) > 100 {
			label = label[:100]
		}
		out = append(out, &discordgo.ApplicationCommandOptionChoice{Name: string(label), Value: value})
		if len(out) == maxChoices {
			break
		}
	}
	return out
}

// handleCollab tags a stream: who was there, and who was not.
func (r *Router) handleCollab(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if !r.inDevChannel(i) {
		return reply(s, i, msg.DevOnly)
	}
	if r.deps.Collab == nil {
		return reply(s, i, msg.CollabOff)
	}

	var stream, add, remove string
	for _, opt := range i.ApplicationCommandData().Options {
		switch opt.Name {
		case "stream":
			stream = opt.StringValue()
		case "add":
			add = opt.StringValue()
		case "remove":
			remove = opt.StringValue()
		}
	}

	out, err := r.deps.Collab.Change(collabs.VideoID(stream), add, remove)
	switch {
	case errors.Is(err, collabs.ErrNoStream):
		return reply(s, i, msg.CollabNoStream)
	case errors.Is(err, collabs.ErrUnknownNames):
		return reply(s, i, msg.CollabUnknown(out.Unknown))
	}

	text := msg.CollabDone(out.Stream.Title, out.Stream.Start, out.Stream.Partners, out.Added, out.Removed, userName(i))
	if err != nil {
		// The change holds in memory; only the file could not be written.
		_ = reply(s, i, text+"\n"+msg.CollabNotSaved)
		return fmt.Errorf("tag collab: %w", err)
	}
	return reply(s, i, text)
}

// userName is who ran the command, as shown in the server.
func userName(i *discordgo.InteractionCreate) string {
	switch {
	case i.Member == nil:
		return ""
	case i.Member.Nick != "":
		return i.Member.Nick
	case i.Member.User != nil:
		return i.Member.User.Username
	}
	return ""
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
