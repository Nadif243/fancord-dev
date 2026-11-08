package discord

import (
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/settings"
)

// SettingsAdmin is what /setup changes. *settings.Live satisfies it.
type SettingsAdmin interface {
	Where(k settings.Key) (string, settings.Source)
	Set(k settings.Key, id string) error
	Reset(k settings.Key) (bool, error)
	PingUpcoming() string
	PingLive() string
}

// Discord permission bits the bot needs in the channels it is given.
const (
	permViewChannel         int64 = 1 << 10
	permSendMessages        int64 = 1 << 11
	permAddReactions        int64 = 1 << 6
	permReadMessageHistory  int64 = 1 << 16
	needToPost                    = permViewChannel | permSendMessages
	needInFanChannel              = needToPost | permAddReactions | permReadMessageHistory
	setupDefaultPermissions       = permManageGuild // who sees /setup in the command list
)

// canSetup says whether this member may use /setup.
func canSetup(m *discordgo.Member) bool {
	return m != nil && m.Permissions&(permAdministrator|permManageGuild) != 0
}

// addSetup registers /setup with its four subcommands.
func (r *Router) addSetup() {
	which := func(kind settings.Kind) []*discordgo.ApplicationCommandOptionChoice {
		var out []*discordgo.ApplicationCommandOptionChoice
		for _, i := range settings.All {
			if i.Kind == kind {
				out = append(out, &discordgo.ApplicationCommandOptionChoice{Name: i.Label, Value: string(i.Key)})
			}
		}
		return out
	}
	all := append(which(settings.ChannelKind), which(settings.RoleKind)...)
	pick := func(choices []*discordgo.ApplicationCommandOptionChoice) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionString, Name: "which", Description: msg.SetupWhichDescription,
			Required: true, Choices: choices,
		}
	}
	perm := setupDefaultPermissions
	r.add(&discordgo.ApplicationCommand{
		Name:                     "setup",
		Description:              msg.SetupDescription,
		DefaultMemberPermissions: &perm,
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "show", Description: msg.SetupShowDescription},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "channel", Description: msg.SetupChannelDescription,
				Options: []*discordgo.ApplicationCommandOption{
					pick(which(settings.ChannelKind)),
					{
						Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: msg.SetupChannelOption, Required: true,
						ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews},
					},
				},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "role", Description: msg.SetupRoleDescription,
				Options: []*discordgo.ApplicationCommandOption{
					pick(which(settings.RoleKind)),
					{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: msg.SetupRoleOption, Required: true},
				},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "reset", Description: msg.SetupResetDescription,
				Options: []*discordgo.ApplicationCommandOption{pick(all)},
			},
		},
	}, r.handleSetup)
}

func (r *Router) handleSetup(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if r.deps.Settings == nil {
		return replyPrivate(s, i, msg.SetupOff)
	}
	if !canSetup(i.Member) {
		return replyPrivate(s, i, msg.SetupDenied)
	}
	subs := i.ApplicationCommandData().Options
	if len(subs) == 0 {
		return replyPrivate(s, i, msg.SetupBad)
	}
	sub := subs[0]
	env := setupEnv{Listening: r.deps.ListeningForMessages, ReviewOn: r.deps.Outside != nil, GuildID: i.GuildID}

	switch sub.Name {
	case "show":
		return replyPrivate(s, i, setupShow(r.deps.Settings, env))
	case "reset":
		return replyPrivate(s, i, setupReset(r.deps.Settings, optValue(sub.Options, "which")))
	case "channel", "role":
		id := optValue(sub.Options, sub.Name) // the option is named like the subcommand
		key := settings.Key(optValue(sub.Options, "which"))
		if sub.Name == "channel" {
			env.MissingPerms = botMissing(s, id, neededPerms(key))
		}
		return replyPrivate(s, i, setupSave(r.deps.Settings, env, key, sub.Name == "role", id))
	}
	return replyPrivate(s, i, msg.SetupBad)
}

// optValue reads one option's value as text (channel and role options carry their ID as text).
func optValue(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			v, _ := o.Value.(string)
			return v
		}
	}
	return ""
}

// neededPerms is what the bot must be allowed to do in the channel for a setting: the fan channel is read and reacted to, the others only posted in.
func neededPerms(key settings.Key) int64 {
	if key == settings.Fans {
		return needInFanChannel
	}
	return needToPost
}

// botMissing reports whether the bot lacks any of the permission bits in the channel. When it cannot tell, it says no: this only decides whether to add a warning, never whether to save.
func botMissing(s *discordgo.Session, channelID string, need int64) bool {
	if s == nil || s.State == nil || s.State.User == nil || channelID == "" {
		return false
	}
	have, err := s.UserChannelPermissions(s.State.User.ID, channelID)
	if err != nil {
		return false
	}
	return have&need != need
}

// setupEnv is what the answers depend on besides the settings themselves.
type setupEnv struct {
	Listening    bool   // the bot asked for the Message Content intent at start
	ReviewOn     bool   // collab review exists (YouTube is set up)
	MissingPerms bool   // the bot lacks permissions in the channel just chosen
	GuildID      string // the server's ID, which is also the ID of its @everyone role
}

func mentionOf(kind settings.Kind, id string) string {
	switch {
	case id == "":
		return ""
	case kind == settings.RoleKind:
		return "<@&" + id + ">"
	}
	return "<#" + id + ">"
}

func sourceNote(src settings.Source, fallback settings.Key) string {
	switch src {
	case settings.FromSetup:
		return "set by /setup"
	case settings.FromEnv:
		return ".env"
	case settings.FromOther:
		if i, ok := settings.Lookup(fallback); ok {
			return "same as " + i.Label
		}
		return "same as another setting"
	}
	return ""
}

// setupShow lists every setting with its value and where it comes from.
func setupShow(a SettingsAdmin, env setupEnv) string {
	var rows []msg.SetupRow
	for _, info := range settings.All {
		if info.Key == settings.Ping {
			rows = append(rows, pingRow(a, info))
			continue
		}
		id, src := a.Where(info.Key)
		rows = append(rows, msg.SetupRow{Label: info.Label, Mention: mentionOf(info.Kind, id), Note: sourceNote(src, info.Fallback)})
	}
	var warn []string
	if id, _ := a.Where(settings.Fans); id != "" {
		switch {
		case !env.ReviewOn:
			warn = append(warn, msg.SetupWarnFansNoYT)
		case !env.Listening:
			warn = append(warn, msg.SetupWarnFansRestart)
		}
	}
	return msg.SetupShow(rows, warn)
}

// pingRow shows the ping role. The two old .env settings can still give two different roles, so both are shown when they differ.
func pingRow(a SettingsAdmin, info settings.Info) msg.SetupRow {
	up, live := a.PingUpcoming(), a.PingLive()
	_, src := a.Where(settings.Ping)
	switch {
	case up == "" && live == "":
		return msg.SetupRow{Label: info.Label}
	case up == live:
		note := ".env"
		if src == settings.FromSetup {
			note = "set by /setup"
		}
		return msg.SetupRow{Label: info.Label, Mention: mentionOf(settings.RoleKind, up), Note: note}
	}
	text := fmt.Sprintf("waiting room %s, live %s", orNone(mentionOf(settings.RoleKind, up)), orNone(mentionOf(settings.RoleKind, live)))
	return msg.SetupRow{Label: info.Label, Mention: text, Note: ".env, two roles; /setup role replaces both"}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// setupSave saves one channel or role and says what it means.
func setupSave(a SettingsAdmin, env setupEnv, key settings.Key, isRole bool, id string) string {
	info, ok := settings.Lookup(key)
	if !ok || id == "" || (info.Kind == settings.RoleKind) != isRole {
		return msg.SetupBad
	}
	if isRole && id == env.GuildID {
		return msg.SetupEveryone // as a ping it would hit everybody; as the reviewer role it would match nobody
	}
	mention := mentionOf(info.Kind, id)

	var notes []string
	if err := a.Set(key, id); err != nil {
		if !isSaveError(err) {
			return msg.SetupBad
		}
		notes = append(notes, msg.SetupNoSave)
	}
	if env.MissingPerms {
		if key == settings.Fans {
			notes = append(notes, fmt.Sprintf(msg.SetupPermsFans, mention))
		} else {
			notes = append(notes, fmt.Sprintf(msg.SetupPermsPost, mention))
		}
	}
	switch key {
	case settings.Announce:
		notes = append(notes, msg.SetupNoteAnnounce)
	case settings.Review:
		notes = append(notes, msg.SetupNoteReview)
	case settings.Ops:
		notes = append(notes, msg.SetupNoteOps)
	case settings.Staff:
		notes = append(notes, fmt.Sprintf(msg.SetupNoteStaff, mention))
	case settings.Reminder:
		notes = append(notes, msg.SetupNoteReminder)
	case settings.Ping:
		notes = append(notes, msg.SetupNotePing)
	case settings.TweetPing:
		notes = append(notes, msg.SetupNoteTweetPing)
	case settings.Reviewer:
		notes = append(notes, msg.SetupNoteReviewer)
	case settings.Fans:
		switch {
		case !env.ReviewOn:
			notes = append(notes, msg.SetupNoteFansNoYT)
		case !env.Listening:
			notes = append(notes, msg.SetupNoteFansRestart)
		}
	}
	return msg.SetupSaved(info.Label, mention, notes)
}

// isSaveError tells "could not write the file" (the value is still in force) from "the value was refused".
func isSaveError(err error) bool {
	return !errors.Is(err, settings.ErrRefused)
}

// setupReset forgets a saved value.
func setupReset(a SettingsAdmin, which string) string {
	key := settings.Key(which)
	info, ok := settings.Lookup(key)
	if !ok {
		return msg.SetupBad
	}
	had, err := a.Reset(key)
	text := msg.SetupReset(info.Label, "", had)
	if had {
		id, _ := a.Where(key)
		text = msg.SetupReset(info.Label, mentionOf(info.Kind, id), true)
	}
	if err != nil {
		text += "\n" + msg.SetupNoSave
	}
	return text
}
