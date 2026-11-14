package discord

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/rolepanel"
)

// RolePanel is the role-choosing panel. *rolepanel.Service satisfies it.
type RolePanel interface {
	Press(roleID string, has []string) (rolepanel.Action, error)
	Categories() []rolepanel.Category
	Channel() string
	SetCategory(name string, one bool, title string) (created bool, c rolepanel.Category, err error)
	SetRole(category string, r rolepanel.Role) (moved bool, err error)
	RemoveRole(roleID string) (rolepanel.Role, bool, error)
	Publish(guildID, channelID string) (rolepanel.Report, error)
}

const panelColor = 0xEF8854

// Permission bits that make a role unfit to be handed out by a button.
const riskyPerms int64 = 1<<1 | // kick members
	1<<2 | // ban members
	1<<3 | // administrator
	1<<4 | // manage channels
	1<<5 | // manage server
	1<<13 | // manage messages
	1<<17 | // mention @everyone
	1<<28 | // manage roles
	1<<29 | // manage webhooks
	1<<34 | // manage threads
	1<<40 // time out members

// unsafeRole says whether a role must not be on the panel.
func unsafeRole(r *discordgo.Role, guildID string) bool {
	return r.ID == guildID || r.Managed || r.Permissions&riskyPerms != 0
}

// PanelAdapter posts the role panel and reads the server's roles, so the rolepanel package does not need to know about the library.
type PanelAdapter struct {
	S *discordgo.Session
}

func (a PanelAdapter) PostPanel(channelID string, v rolepanel.View) (string, error) {
	sent, err := a.S.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{panelEmbed(v)},
		Components:      panelRows(v),
		AllowedMentions: nobody(),
	})
	if err != nil {
		return "", err
	}
	return sent.ID, nil
}

func (a PanelAdapter) EditPanel(channelID, messageID string, v rolepanel.View) error {
	embeds := []*discordgo.MessageEmbed{panelEmbed(v)}
	rows := panelRows(v)
	_, err := a.S.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:         channelID,
		ID:              messageID,
		Embeds:          &embeds,
		Components:      &rows,
		AllowedMentions: nobody(),
	})
	if isGone(err) {
		return fmt.Errorf("%w: %v", rolepanel.ErrGone, err)
	}
	return err
}

func (a PanelAdapter) DeletePanel(channelID, messageID string) error {
	err := a.S.ChannelMessageDelete(channelID, messageID)
	if isGone(err) {
		return nil
	}
	return err
}

func (a PanelAdapter) Roles(guildID string) (map[string]rolepanel.GuildRole, error) {
	roles, err := a.S.GuildRoles(guildID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]rolepanel.GuildRole, len(roles))
	for _, r := range roles {
		out[r.ID] = rolepanel.GuildRole{Name: r.Name, Unsafe: unsafeRole(r, guildID)}
	}
	return out, nil
}

// panelEmbed is one group's message: the heading, the line under it, and one field per role in three columns.
func panelEmbed(v rolepanel.View) *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{Title: v.Title, Description: v.Description, Color: panelColor}
	for _, f := range v.Fields {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: f.Name, Value: f.Value, Inline: true})
	}
	return e
}

// panelRows lays the buttons out five to a row.
func panelRows(v rolepanel.View) []discordgo.MessageComponent {
	var rows []discordgo.MessageComponent
	var row discordgo.ActionsRow
	for _, b := range v.Buttons {
		row.Components = append(row.Components, discordgo.Button{
			Label:    b.Label,
			Style:    discordgo.SecondaryButton,
			CustomID: b.CustomID,
			Emoji:    &discordgo.ComponentEmoji{Name: b.Emoji.Name, ID: b.Emoji.ID, Animated: b.Emoji.Animated},
		})
		if len(row.Components) == 5 {
			rows = append(rows, row)
			row = discordgo.ActionsRow{}
		}
	}
	if len(row.Components) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// onRole handles a press on a role button: the person gets the role, or loses it again. Only they see the answer.
func (r *Router) onRole(s *discordgo.Session, i *discordgo.InteractionCreate, roleID string) {
	log := r.log.With("button", "role", "role", roleID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("button panicked", "panic", rec)
			_ = replyPrivate(s, i, msg.RoleFailed)
		}
	}()
	if r.deps.Roles == nil {
		_ = replyPrivate(s, i, msg.RoleOff)
		return
	}
	if i.Member == nil || i.Member.User == nil || i.GuildID == "" {
		_ = replyPrivate(s, i, msg.RoleFailed)
		return
	}
	act, err := r.deps.Roles.Press(roleID, i.Member.Roles)
	if errors.Is(err, rolepanel.ErrGone) {
		_ = replyPrivate(s, i, msg.RoleUnavailable)
		return
	}
	userID := i.Member.User.ID
	// The new role first: if that fails nothing has changed.
	for _, id := range act.Add {
		if err = s.GuildMemberRoleAdd(i.GuildID, userID, id); err != nil {
			break
		}
	}
	if err == nil {
		for _, id := range act.Remove {
			if err = s.GuildMemberRoleRemove(i.GuildID, userID, id); err != nil {
				break
			}
		}
	}
	if err != nil {
		log.Error("could not change the roles", "err", err)
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == http.StatusForbidden {
			_ = replyPrivate(s, i, msg.RoleNoPermission)
			return
		}
		_ = replyPrivate(s, i, msg.RoleFailed)
		return
	}
	_ = replyPrivate(s, i, act.Text)
}

// addRolePanel registers /rolepanel: the staff command that sets up the panel.
func (r *Router) addRolePanel() {
	perm := setupDefaultPermissions
	group := &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionString, Name: "group", Description: msg.RolePanelGroupOption,
		Required: true, Autocomplete: true,
	}
	role := &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: msg.RolePanelRoleOption, Required: true,
	}
	r.add(&discordgo.ApplicationCommand{
		Name:                     "rolepanel",
		Description:              msg.RolePanelCmdDescription,
		DefaultMemberPermissions: &perm,
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "show", Description: msg.RolePanelShowDescription},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "add", Description: msg.RolePanelAddDescription,
				Options: []*discordgo.ApplicationCommandOption{
					group, role,
					{Type: discordgo.ApplicationCommandOptionString, Name: "emoji", Description: msg.RolePanelEmojiOption, Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "description", Description: msg.RolePanelDescOption},
				},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "remove", Description: msg.RolePanelRemoveDescription,
				Options: []*discordgo.ApplicationCommandOption{role},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "category", Description: msg.RolePanelCategoryDescription,
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: msg.RolePanelNameOption, Required: true},
					{
						Type: discordgo.ApplicationCommandOptionString, Name: "rule", Description: msg.RolePanelRuleOption, Required: true,
						Choices: []*discordgo.ApplicationCommandOptionChoice{
							{Name: msg.RolePanelRuleAny, Value: "any"},
							{Name: msg.RolePanelRuleOne, Value: "one"},
						},
					},
					{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: msg.RolePanelTitleOption},
				},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "publish", Description: msg.RolePanelPublishDescription,
				Options: []*discordgo.ApplicationCommandOption{{
					Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: msg.RolePanelChannelOption,
					ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews},
				}},
			},
		},
	}, r.handleRolePanel)
	r.suggest["rolepanel"] = r.rolePanelChoices
}

// rolePanelChoices offers the groups while "group" is being typed.
func (r *Router) rolePanelChoices(i *discordgo.InteractionCreate) []*discordgo.ApplicationCommandOptionChoice {
	if r.deps.Roles == nil || !canSetup(i.Member) {
		return nil
	}
	subs := i.ApplicationCommandData().Options
	if len(subs) == 0 {
		return nil
	}
	for _, opt := range subs[0].Options {
		if !opt.Focused || opt.Name != "group" {
			continue
		}
		typed := rolepanel.Key(opt.StringValue())
		var out []*discordgo.ApplicationCommandOptionChoice
		for _, c := range r.deps.Roles.Categories() {
			if typed == "" || containsText(c.Key, typed) {
				out = append(out, &discordgo.ApplicationCommandOptionChoice{Name: c.Name, Value: c.Key})
			}
		}
		if len(out) > maxChoices {
			out = out[:maxChoices]
		}
		return out
	}
	return nil
}

func containsText(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func (r *Router) handleRolePanel(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	if r.deps.Roles == nil {
		return replyPrivate(s, i, msg.RolePanelOff)
	}
	if !canSetup(i.Member) {
		return replyPrivate(s, i, msg.RolePanelDenied)
	}
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return replyPrivate(s, i, msg.RolePanelBad)
	}
	sub := data.Options[0]
	switch sub.Name {
	case "show":
		return replyPrivate(s, i, r.rolePanelShow())
	case "add":
		return replyPrivate(s, i, r.rolePanelAdd(i, data, sub.Options))
	case "remove":
		return replyPrivate(s, i, r.rolePanelRemove(optValue(sub.Options, "role")))
	case "category":
		return replyPrivate(s, i, r.rolePanelCategory(sub.Options))
	case "publish":
		// Posting several messages can take a few seconds, and Discord wants an answer in three.
		if err := deferPrivate(s, i); err != nil {
			return err
		}
		return editReply(s, i, r.rolePanelPublish(s, i, optValue(sub.Options, "channel")))
	}
	return replyPrivate(s, i, msg.RolePanelBad)
}

// deferPrivate shows "the bot is thinking..." to the person who typed the command only.
func deferPrivate(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
}

func (r *Router) rolePanelShow() string {
	var cats []msg.RolePanelCat
	for _, c := range r.deps.Roles.Categories() {
		row := msg.RolePanelCat{Name: c.Name, Key: c.Key, Title: c.Title, One: c.One, Posted: c.MessageID != ""}
		for _, ro := range c.Roles {
			row.Roles = append(row.Roles, msg.RolePanelRoleLine{ID: ro.ID, Emoji: ro.Emoji, Desc: ro.Desc})
		}
		cats = append(cats, row)
	}
	return msg.RolePanelShow(r.deps.Roles.Channel(), cats)
}

func (r *Router) rolePanelAdd(i *discordgo.InteractionCreate, data discordgo.ApplicationCommandInteractionData, opts []*discordgo.ApplicationCommandInteractionDataOption) string {
	roleID := optValue(opts, "role")
	var role *discordgo.Role
	if data.Resolved != nil {
		role = data.Resolved.Roles[roleID]
	}
	switch {
	case role == nil:
		return msg.RolePanelBad
	case role.ID == i.GuildID:
		return msg.RolePanelEveryone
	case role.Managed:
		return msg.RolePanelManaged
	case role.Permissions&riskyPerms != 0:
		return msg.RolePanelRisky(roleID)
	}
	group := optValue(opts, "group")
	moved, err := r.deps.Roles.SetRole(group, rolepanel.Role{
		ID: roleID, Name: role.Name, Emoji: optValue(opts, "emoji"), Desc: optValue(opts, "description"),
	})
	switch {
	case errors.Is(err, rolepanel.ErrBadEmoji):
		return msg.RolePanelBadEmoji
	case errors.Is(err, rolepanel.ErrNoCategory):
		return msg.RolePanelNoGroup
	case errors.Is(err, rolepanel.ErrFull):
		return msg.RolePanelFull
	}
	text := msg.RolePanelAdded(roleID, groupName(r.deps.Roles, group), moved)
	if err != nil {
		text += msg.RolePanelNoSave // the change holds until restart
	}
	return text
}

// groupName is the name a group is shown with, found from what was typed.
func groupName(p RolePanel, typed string) string {
	key := rolepanel.Key(typed)
	for _, c := range p.Categories() {
		if c.Key == key {
			return c.Name
		}
	}
	return typed
}

func (r *Router) rolePanelRemove(roleID string) string {
	_, found, err := r.deps.Roles.RemoveRole(roleID)
	if !found {
		return msg.RolePanelNotOnIt
	}
	text := msg.RolePanelRemoved(roleID)
	if err != nil {
		text += msg.RolePanelNoSave
	}
	return text
}

func (r *Router) rolePanelCategory(opts []*discordgo.ApplicationCommandInteractionDataOption) string {
	name := optValue(opts, "name")
	one := optValue(opts, "rule") == "one"
	created, c, err := r.deps.Roles.SetCategory(name, one, optValue(opts, "title"))
	if errors.Is(err, rolepanel.ErrNoCategory) {
		return msg.RolePanelBad
	}
	text := msg.RolePanelGroupSaved(c.Name, created, one)
	if err != nil {
		text += msg.RolePanelNoSave
	}
	return text
}

// rolePanelPublish posts or refreshes the panel and says what happened.
func (r *Router) rolePanelPublish(s *discordgo.Session, i *discordgo.InteractionCreate, channelID string) string {
	if channelID == "" {
		channelID = r.deps.Roles.Channel()
	}
	if channelID == "" {
		return msg.RolePanelNoChan
	}
	rep, err := r.deps.Roles.Publish(i.GuildID, channelID)
	if errors.Is(err, rolepanel.ErrNoChannel) {
		return msg.RolePanelNoChan
	}
	if err != nil {
		r.log.Error("could not publish the role panel", "err", err)
		return msg.RolePanelPublishFailed(err)
	}
	return msg.RolePanelPublished(rep.ChannelID, rep.Posted, rep.Updated, rep.Deleted, rep.Fixed, rep.Missing, rep.Unsafe)
}
