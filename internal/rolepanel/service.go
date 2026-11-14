package rolepanel

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

// Button is one button under a group's message.
type Button struct {
	Label    string
	Emoji    Emoji
	CustomID string
}

// Field is one role in the message: "🎨 = Tukang Gambar" over its description.
type Field struct{ Name, Value string }

// View is a group's message as the discord package posts it.
type View struct {
	Title       string
	Description string
	Fields      []Field
	Buttons     []Button
}

// Poster puts the panel in a channel. *discord.PanelAdapter satisfies it.
type Poster interface {
	PostPanel(channelID string, v View) (messageID string, err error)
	// EditPanel reports ErrGone (wrapped) when the message does not exist.
	EditPanel(channelID, messageID string, v View) error
	// DeletePanel is not an error when the message is already gone.
	DeletePanel(channelID, messageID string) error
}

// GuildRole is a role of the server as the panel needs to know it.
type GuildRole struct {
	Name string
	// Unsafe roles are never handed out by the panel: those that manage the server, its members or its messages, and those a bot or integration owns.
	Unsafe bool
}

// Guild is the server's roles. *discord.PanelAdapter satisfies it.
type Guild interface {
	Roles(guildID string) (map[string]GuildRole, error) // by role ID
}

// Service combines the stored groups with Discord.
type Service struct {
	store  *Store
	poster Poster
	guild  Guild
	pub    sync.Mutex // one publish at a time
}

func New(store *Store, poster Poster, guild Guild) *Service {
	return &Service{store: store, poster: poster, guild: guild}
}

// Categories and Channel are the stored setup, for /rolepanel show.
func (s *Service) Categories() []Category { return s.store.Categories() }
func (s *Service) Channel() string        { return s.store.Channel() }

func (s *Service) SetCategory(name string, one bool, title string) (bool, Category, error) {
	return s.store.SetCategory(name, one, title)
}
func (s *Service) SetRole(category string, r Role) (bool, error) { return s.store.SetRole(category, r) }
func (s *Service) RemoveRole(roleID string) (Role, bool, error)  { return s.store.RemoveRole(roleID) }

// customPrefix starts the ID of every panel button: "role:<role ID>".
const customPrefix = "role:"

// ParseCustomID undoes a button's ID: ok is false for any other component.
func ParseCustomID(id string) (roleID string, ok bool) {
	roleID, ok = strings.CutPrefix(id, customPrefix)
	return roleID, ok && roleID != ""
}

// BuildView is the message of one group.
func BuildView(c Category) View {
	v := View{Title: c.Title, Description: msg.RolePanelDescription}
	for _, r := range c.Roles {
		value := r.Desc
		if value == "" {
			value = "​" // a field may not be empty
		}
		// The role's own name may already start with an emoji; the panel shows the one chosen here, so the name is shown without it.
		name := DisplayName(r.Name)
		v.Fields = append(v.Fields, Field{Name: r.Emoji + " = " + name, Value: value})
		e, _ := ParseEmoji(r.Emoji)
		v.Buttons = append(v.Buttons, Button{Label: shorten(name, 40), Emoji: e, CustomID: customPrefix + r.ID})
	}
	return v
}

// Action is what a button press changes for the person who pressed it.
type Action struct {
	Add    []string // role IDs to give
	Remove []string // role IDs to take away
	Text   string   // what to tell them, privately
}

// Press decides what pressing a role's button does, given the roles the person has now: they get the role, or lose it when they had it. In a "one" group they also lose the others of the group. ErrGone means the role is not on the panel any more.
func (s *Service) Press(roleID string, has []string) (Action, error) {
	c, r, ok := s.store.Find(roleID)
	if !ok {
		return Action{}, ErrGone
	}
	if contains(has, roleID) {
		return Action{Remove: []string{roleID}, Text: msg.RoleLost(r.Name)}, nil
	}
	a := Action{Add: []string{roleID}}
	var dropped []string
	if c.One {
		for _, other := range c.Roles {
			if other.ID != roleID && contains(has, other.ID) {
				a.Remove = append(a.Remove, other.ID)
				dropped = append(dropped, other.Name)
			}
		}
	}
	a.Text = msg.RoleGot(r.Name, dropped)
	return a, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ErrNoChannel: nowhere to post yet.
var ErrNoChannel = errors.New("no channel for the panel")

// Report is what a publish did.
type Report struct {
	ChannelID string
	Posted    int      // messages sent new
	Updated   int      // messages edited in place
	Deleted   int      // messages of groups that are empty now
	Fixed     []string // roles whose ID was found again by name
	Missing   []string // roles the server does not have; left out of the message
	Unsafe    []string // roles that must not be handed out; left out of the message
}

// Publish posts the panel, or brings the posted messages up to date: a group whose message exists is edited, one without a message (or whose message was deleted) is posted. Groups with no role to show are not posted. Role names are read from the server first, and a role whose ID the server does not know is found again by its name when it has the same name.
func (s *Service) Publish(guildID, channelID string) (Report, error) {
	s.pub.Lock()
	defer s.pub.Unlock()

	if channelID == "" {
		channelID = s.store.Channel()
	}
	if channelID == "" {
		return Report{}, ErrNoChannel
	}
	roles, err := s.guild.Roles(guildID)
	if err != nil {
		return Report{}, fmt.Errorf("read the server's roles: %w", err)
	}
	// A role is found again by its exact name first; failing that, by its name without emoji, so "Tukang Gambar" still finds "🎨 Tukang Gambar".
	byName, byPlain := map[string]string{}, map[string]string{}
	for id, g := range roles {
		byName[strings.ToLower(strings.TrimSpace(g.Name))] = id
		byPlain[plainKey(g.Name)] = id
	}

	rep := Report{ChannelID: channelID}
	renames := map[string]Role{} // old ID -> the role as it should be stored
	if err := s.store.update(func(d *fileData) bool { d.ChannelID = channelID; return true }); err != nil {
		return rep, err
	}
	for _, c := range s.store.Categories() {
		shown := c
		shown.Roles = nil
		for _, r := range c.Roles {
			old := r.ID
			g, ok := roles[r.ID]
			if !ok {
				id, found := byName[strings.ToLower(strings.TrimSpace(r.Name))]
				if !found {
					id, found = byPlain[plainKey(r.Name)]
				}
				if !found {
					rep.Missing = append(rep.Missing, r.Name)
					continue
				}
				r.ID, g = id, roles[id]
				rep.Fixed = append(rep.Fixed, g.Name)
			}
			r.Name = g.Name
			if g.Unsafe {
				rep.Unsafe = append(rep.Unsafe, g.Name)
				continue
			}
			renames[old] = r
			shown.Roles = append(shown.Roles, r)
		}

		err := s.publishOne(&rep, c, shown, channelID)
		if err != nil {
			return rep, fmt.Errorf("group %q: %w", c.Name, err)
		}
	}
	// Names and IDs found above are kept, so the next publish starts from them.
	err = s.store.update(func(d *fileData) bool {
		for i := range d.Categories {
			for j, r := range d.Categories[i].Roles {
				if fixed, ok := renames[r.ID]; ok {
					d.Categories[i].Roles[j].ID, d.Categories[i].Roles[j].Name = fixed.ID, fixed.Name
				}
			}
		}
		return true
	})
	return rep, err
}

// plainKey is a role name as two names are compared: without emoji, lower case.
func plainKey(name string) string {
	return strings.ToLower(strings.TrimSpace(DisplayName(name)))
}

// publishOne brings one group's message up to date. old is the group as stored; shown holds only the roles the server has.
func (s *Service) publishOne(rep *Report, old, shown Category, channelID string) error {
	setMessage := func(ch, id string) error {
		return s.store.update(func(d *fileData) bool {
			for i := range d.Categories {
				if d.Categories[i].Key == old.Key {
					d.Categories[i].ChannelID, d.Categories[i].MessageID = ch, id
				}
			}
			return true
		})
	}
	posted := old.MessageID != ""

	if len(shown.Roles) == 0 {
		if posted {
			if err := s.poster.DeletePanel(old.ChannelID, old.MessageID); err != nil {
				return err
			}
			rep.Deleted++
			return setMessage("", "")
		}
		return nil
	}
	v := BuildView(shown)
	if posted && old.ChannelID == channelID {
		err := s.poster.EditPanel(channelID, old.MessageID, v)
		if err == nil {
			rep.Updated++
			return nil
		}
		if !errors.Is(err, ErrGone) {
			return err
		}
	} else if posted {
		_ = s.poster.DeletePanel(old.ChannelID, old.MessageID) // the panel moved to another channel
	}
	id, err := s.poster.PostPanel(channelID, v)
	if err != nil {
		return err
	}
	rep.Posted++
	return setMessage(channelID, id)
}
