// Package config reads the bot's settings from environment variables.
//
// Load takes the lookup function as an argument (os.Getenv in production, a map lookup in tests), so it never touches the real environment itself.
package config

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/settings"
)

// A YouTube channel ID is "UC" followed by 22 letters, digits, "-" or "_".
var youtubeChannelID = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

// Where the bot keeps its files unless the .env says otherwise.
const (
	defaultDataFile      = "data/state.json"
	defaultHistoryFile   = "data/history.json"
	defaultCollabFile    = "data/collabs.json"
	defaultOutsideFile   = "data/outside.json"
	defaultWhichFile     = "data/which.json"
	defaultRolesFile     = "data/roles.json"
	defaultRodokFile     = "data/rodok.json"
	defaultSettingsFile  = "data/settings.json"
	defaultCommunityFile = "data/community.json"
	defaultPartnersFile  = "partners.csv"
)

// How often things are checked, in seconds, and the least that is allowed.
const (
	defaultPollSeconds          = 120 // her YouTube channel
	minPollSeconds              = 30
	defaultCommunityPollSeconds = 300 // checked less often: nobody needs it to the minute, and every check costs quota
	defaultXPollSeconds         = 300
	minXPollSeconds             = 60
	defaultHeartbeatSeconds     = 60
	minHeartbeatSeconds         = 15
)

// Other defaults.
const (
	defaultMentionWords  = "yuura,yozakura"
	defaultXUsername     = "YozakuraYuura"
	defaultXMonthlyCap   = 250
	minXMonthlyCap       = 10
	defaultXFanLookupCap = 20
	defaultTranslateTo   = "Indonesian"
)

// Channels are the places the bot posts to. They are separate settings so that in production each can be its own channel, while in dev they can all point at the one test channel.
type Channels struct {
	Test     string // main dev channel: commands, embeds, buttons
	Ops      string // heartbeat and alerts
	Announce string // stream and tweet announcements
	Approval string // mod approval queue
	Recap    string // the weekly recap; the announcement channel when not set
	Tweets   string // her X posts; the announcement channel when not set

	// CollabLinks is where fans send collab links as ordinary messages. Empty means off; it needs the Message Content intent (see .env.example).
	CollabLinks string

	// Reminder is the Saturday reminder's own channel. Empty: the staff channel (Test), where it always went before.
	Reminder string
}

// X holds the settings for relaying her X posts. Without a token the relay is simply off.
type X struct {
	BearerToken string        // secret
	Username    string        // her handle, without the @
	PollEvery   time.Duration // how often X is checked
	Kinds       []string      // post types to relay: post, reply, quote, repost
	MonthlyCap  int           // billed reads per month before the relay pauses

	// FanLookupCap is how many X posts per month may be read for links fans send (one billed read each). 0 turns reading them off.
	FanLookupCap int

	// Style is how a post is shown: "card" (a card with the text, pictures, numbers and translation) or "link" (a plain link that Discord previews).
	Style string
}

// Translate holds the settings for translating Japanese posts. Without a key nothing is translated.
type Translate struct {
	APIKey string // secret
	Model  string // the Gemini model to ask
	To     string // the language to translate into, named in English
}

// Review holds the settings for collabs on other channels: candidates are posted in the approval channel and confirmed with buttons.
type Review struct {
	RoleID         string   // who may press the buttons (optional, see discord.canReview)
	File           string   // where the decisions are kept
	MentionWords   []string // words that show a video is about her
	IgnoreChannels []string // YouTube channel IDs that are never shown
}

// YouTube holds the settings for reading her channel. Both are optional, so the bot still starts without them; the commands that need them say so.
type YouTube struct {
	APIKey    string        // secret
	ChannelID string        // her channel, starts with UC
	PollEvery time.Duration // how often the poller checks it
}

// Community is the community's own YouTube channel, announced next to hers.
// It is optional: without a channel ID nothing here is used. It is a plain channel: no recap, no collab partners.
type Community struct {
	ChannelID string        // starts with UC; empty means off
	Name      string        // how its messages call it
	PollEvery time.Duration // how often it is checked
	File      string        // what it announced (its own file, apart from hers)

	// Where its messages go, and the role it pings (for waiting rooms, live streams and new uploads alike). Both optional: no channel means the announcement channel, no role means no ping.
	Channel string
	Role    string
}

// Pings are the roles the bot mentions. Both are optional: no role set, no ping.
type Pings struct {
	Upcoming string // a new waiting room appeared
	Live     string // a stream went live

	// Both is one role for both; the two above, when set, win over it.
	Both string

	// Tweets is the role pinged for each relayed tweet. Empty: no ping.
	Tweets string
}

type Config struct {
	DataFile       string // where the bot remembers what it announced
	HistoryFile    string // where it records every stream it sees, for the recap
	PartnersFile   string // the collab partner list (CSV) the recap reads
	WhichFile      string // the "who is meant?" questions about shared nicknames
	RolesFile      string // the role panel: groups, roles, emoji and where the messages are
	CollabFile     string // who people tagged as being in a stream, by hand
	RodokFile      string // how many times each person has used /rodok on each other person
	SettingsFile   string // what /setup saved; it beats .env for channels and roles
	Token          string
	AppID          string
	GuildID        string // optional: found from Channels.Test when empty
	Channels       Channels
	Pings          Pings
	YouTube        YouTube
	Community      Community
	X              X
	Translate      Translate
	Review         Review
	HeartbeatEvery time.Duration

	// RodokProtectedRole is the role whose members cannot be rodok-ed: /rodok on them is refused. Empty: nobody is protected.
	RodokProtectedRole string

	// Written is what .env says for the things /setup can change, exactly as written (no defaults filled in). The settings package adds its own defaults and the saved /setup values on top.
	Written settings.Env
}

// loader reads settings and collects every problem it finds, so Load can report them all at once.
type loader struct {
	get      func(string) string
	problems []string
}

func (l *loader) problem(text string) { l.problems = append(l.problems, text) }

// requiredID reads a Discord ID that must be there.
func (l *loader) requiredID(key string) string {
	v := l.get(key)
	if !settings.IsID(v) {
		l.problem(key + " must be a Discord ID (17-20 digits), got " + quote(v))
	}
	return v
}

// id reads an optional Discord ID; fallback is used when it is not written.
func (l *loader) id(key, fallback string) string {
	v := l.get(key)
	if v == "" {
		return fallback
	}
	if !settings.IsID(v) {
		l.problem(key + " must be a Discord ID (17-20 digits), got " + quote(v))
	}
	return v
}

// path reads a file name, or def when it is not written.
func (l *loader) path(key, def string) string {
	if v := l.get(key); v != "" {
		return v
	}
	return def
}

// seconds reads a whole number of seconds, min or more, as a duration.
func (l *loader) seconds(key string, def, min int) time.Duration {
	secs := def
	if v := l.get(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < min {
			l.problem(key + " must be a whole number of " + strconv.Itoa(min) + " or more, got " + quote(v))
		} else {
			secs = n
		}
	}
	return time.Duration(secs) * time.Second
}

// number reads a whole number, min or more; what describes the rule in the error.
func (l *loader) number(key string, def, min int, what string) int {
	if v := l.get(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < min {
			l.problem(key + " must be a whole number" + what + ", got " + quote(v))
			return def
		}
		return n
	}
	return def
}

// youtubeID reads an optional YouTube channel ID.
func (l *loader) youtubeID(key string) string {
	v := l.get(key)
	if v != "" && !youtubeChannelID.MatchString(v) {
		l.problem(key + " must look like UC followed by 22 characters, got " + quote(v))
	}
	return v
}

// Load validates every setting and reports all problems at once, so you fix the .env file in one go instead of one error per run.
func Load(getenv func(string) string) (Config, error) {
	l := &loader{get: func(key string) string { return strings.TrimSpace(getenv(key)) }}
	var c Config

	c.Token = l.get("DISCORD_TOKEN")
	switch {
	case c.Token == "":
		l.problem("DISCORD_TOKEN is empty (Developer Portal > Bot > Reset Token)")
	case strings.HasPrefix(strings.ToLower(c.Token), "bot "):
		l.problem(`DISCORD_TOKEN must be the bare token, without the word "Bot"`)
	}

	c.AppID = l.requiredID("DISCORD_APP_ID")
	c.Channels.Test = l.requiredID("CHANNEL_TEST")
	c.GuildID = l.id("GUILD_ID", "")
	c.Channels.Ops = l.id("CHANNEL_OPS", c.Channels.Test)
	c.Channels.Announce = l.id("CHANNEL_ANNOUNCE", c.Channels.Test)
	c.Channels.Approval = l.id("CHANNEL_APPROVAL", c.Channels.Test)
	c.Channels.Recap = l.id("CHANNEL_RECAP", c.Channels.Announce)
	c.Channels.Tweets = l.id("CHANNEL_TWEETS", c.Channels.Announce)
	c.Channels.CollabLinks = l.id("CHANNEL_COLLAB_LINKS", "")
	c.Channels.Reminder = l.id("CHANNEL_REMINDER", "")
	c.Pings.Upcoming = l.id("ROLE_PING_UPCOMING", "")
	c.Pings.Live = l.id("ROLE_PING_LIVE", "")
	c.Pings.Both = l.id("ROLE_PING", "")
	c.Pings.Tweets = l.id("ROLE_PING_TWEETS", "")
	c.RodokProtectedRole = l.id("ROLE_RODOK_PROTECTED", "")

	// The key is never repeated in an error message, whatever is wrong with it.
	c.YouTube.APIKey = l.get("YOUTUBE_API_KEY")
	c.YouTube.ChannelID = l.youtubeID("YOUTUBE_CHANNEL_ID")
	c.YouTube.PollEvery = l.seconds("YOUTUBE_POLL_SECONDS", defaultPollSeconds, minPollSeconds)

	c.Community = loadCommunity(l)
	c.X = loadX(l)
	c.Translate = loadTranslate(l)

	c.Review.RoleID = l.id("ROLE_COLLAB_REVIEW", "")
	c.Written = settings.Env{
		Values: map[settings.Key]string{
			settings.Announce:  l.get("CHANNEL_ANNOUNCE"),
			settings.Tweets:    l.get("CHANNEL_TWEETS"),
			settings.Recap:     l.get("CHANNEL_RECAP"),
			settings.Fans:      l.get("CHANNEL_COLLAB_LINKS"),
			settings.Review:    l.get("CHANNEL_APPROVAL"),
			settings.Ops:       l.get("CHANNEL_OPS"),
			settings.Staff:     l.get("CHANNEL_TEST"),
			settings.Reminder:  l.get("CHANNEL_REMINDER"),
			settings.Ping:      l.get("ROLE_PING"),
			settings.TweetPing: l.get("ROLE_PING_TWEETS"),
			settings.Reviewer:  l.get("ROLE_COLLAB_REVIEW"),
		},
		PingUpcoming: c.Pings.Upcoming,
		PingLive:     c.Pings.Live,
	}

	words := l.get("COLLAB_MENTION_WORDS")
	if words == "" {
		words = defaultMentionWords
	}
	c.Review.MentionWords = splitList(words)
	for _, id := range splitList(l.get("COLLAB_IGNORE_CHANNELS")) {
		if !youtubeChannelID.MatchString(id) {
			l.problem("COLLAB_IGNORE_CHANNELS must be YouTube channel IDs (UC followed by 22 characters), got " + quote(id))
		} else {
			c.Review.IgnoreChannels = append(c.Review.IgnoreChannels, id)
		}
	}

	c.Review.File = l.path("OUTSIDE_FILE", defaultOutsideFile)
	c.RolesFile = l.path("ROLES_FILE", defaultRolesFile)
	c.WhichFile = l.path("WHICH_FILE", defaultWhichFile)
	c.PartnersFile = l.path("PARTNERS_FILE", defaultPartnersFile)
	c.CollabFile = l.path("COLLAB_FILE", defaultCollabFile)
	c.SettingsFile = l.path("SETTINGS_FILE", defaultSettingsFile)
	c.RodokFile = l.path("RODOK_FILE", defaultRodokFile)
	c.HistoryFile = l.path("HISTORY_FILE", defaultHistoryFile)
	c.DataFile = l.path("DATA_FILE", defaultDataFile)

	c.HeartbeatEvery = l.seconds("HEARTBEAT_SECONDS", defaultHeartbeatSeconds, minHeartbeatSeconds)

	if len(l.problems) > 0 {
		return Config{}, errors.New("invalid configuration:\n  - " + strings.Join(l.problems, "\n  - "))
	}
	return c, nil
}

// splitList splits a comma-separated list, trimming each item and dropping the empty ones.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// An X handle is 1 to 15 letters, digits or underscores.
var xUsername = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// RelayKinds are the post types the relay can pass on, in the order they are listed by default.
var RelayKinds = []string{"post", "reply", "quote", "repost"}

// loadX reads the X settings. The token is never repeated in an error message.
func loadX(l *loader) X {
	x := X{BearerToken: l.get("X_BEARER_TOKEN"), Username: defaultXUsername}

	if v := strings.TrimPrefix(l.get("X_USERNAME"), "@"); v != "" {
		if !xUsername.MatchString(v) {
			l.problem("X_USERNAME must be an X handle (letters, digits and _, up to 15), got " + quote(v))
		} else {
			x.Username = v
		}
	}

	x.PollEvery = l.seconds("X_POLL_SECONDS", defaultXPollSeconds, minXPollSeconds)
	x.MonthlyCap = l.number("X_MONTHLY_READ_CAP", defaultXMonthlyCap, minXMonthlyCap, " of "+strconv.Itoa(minXMonthlyCap)+" or more")
	x.FanLookupCap = l.number("X_FAN_LOOKUP_CAP", defaultXFanLookupCap, 0, ", 0 or more")

	x.Style = "card"
	if v := strings.ToLower(l.get("TWEET_STYLE")); v != "" {
		if v != "card" && v != "link" {
			l.problem("TWEET_STYLE must be card or link, got " + quote(v))
		} else {
			x.Style = v
		}
	}

	x.Kinds = RelayKinds
	if v := l.get("X_RELAY_TYPES"); v != "" {
		x.Kinds = nil
		unknown := false
		for _, k := range splitList(strings.ToLower(v)) {
			switch {
			case !slices.Contains(RelayKinds, k):
				l.problem("X_RELAY_TYPES has an unknown type " + quote(k) + " (use post, reply, quote, repost)")
				unknown = true
			case !slices.Contains(x.Kinds, k):
				x.Kinds = append(x.Kinds, k)
			}
		}
		if len(x.Kinds) == 0 && !unknown {
			l.problem("X_RELAY_TYPES is empty; list at least one of post, reply, quote, repost")
		}
	}
	return x
}

// A language named in English: letters, spaces and hyphens ("Indonesian", "Brazilian Portuguese"). It goes into the translator's instructions, so it is kept to plain words.
var languageName = regexp.MustCompile(`^[A-Za-z][A-Za-z -]{1,39}$`)

// loadTranslate reads the translation settings. The key is never repeated in an error message.
func loadTranslate(l *loader) Translate {
	t := Translate{APIKey: l.get("GEMINI_API_KEY"), Model: l.get("GEMINI_MODEL"), To: defaultTranslateTo}
	if v := l.get("TRANSLATE_TO"); v != "" {
		if !languageName.MatchString(v) {
			l.problem("TRANSLATE_TO must be a language named in English, such as Indonesian or English, got " + quote(v))
		} else {
			t.To = v
		}
	}
	if t.APIKey != "" && t.Model == "" {
		l.problem("GEMINI_MODEL is empty: with GEMINI_API_KEY set, name the model to use")
	}
	return t
}

// loadCommunity reads the community channel's settings. The ID and the name go together: one without the other is a mistake, not "off".
func loadCommunity(l *loader) Community {
	var c Community
	id, name := l.get("COMMUNITY_YOUTUBE_CHANNEL_ID"), l.get("COMMUNITY_NAME")
	switch {
	case id == "" && name == "":
		// off
	case id == "" || name == "":
		l.problem("COMMUNITY_YOUTUBE_CHANNEL_ID and COMMUNITY_NAME go together: set both or neither")
	case !youtubeChannelID.MatchString(id):
		l.problem("COMMUNITY_YOUTUBE_CHANNEL_ID must look like UC followed by 22 characters, got " + quote(id))
	default:
		c.ChannelID, c.Name = id, name
	}
	c.PollEvery = l.seconds("COMMUNITY_POLL_SECONDS", defaultCommunityPollSeconds, minPollSeconds)
	c.File = l.path("COMMUNITY_FILE", defaultCommunityFile)
	c.Channel = l.id("CHANNEL_COMMUNITY", "")
	c.Role = l.id("ROLE_PING_COMMUNITY", "")
	return c
}

func quote(s string) string {
	if s == "" {
		return "nothing"
	}
	return strconv.Quote(s)
}
