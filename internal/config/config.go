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

// A Discord ID ("snowflake") is a number of 17 to 20 digits.
var snowflake = regexp.MustCompile(`^\d{17,20}$`)

// A YouTube channel ID is "UC" followed by 22 letters, digits, "-" or "_".
var youtubeChannelID = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

const (
	defaultDataFile         = "data/state.json"
	defaultHistoryFile      = "data/history.json"
	defaultPartnersFile     = "partners.csv"
	defaultCollabFile       = "data/collabs.json"
	defaultRodokFile        = "data/rodok.json"
	defaultSettingsFile     = "data/settings.json"
	defaultXFanLookupCap    = 20
	defaultOutsideFile      = "data/outside.json"
	defaultMentionWords     = "yuura,yozakura"
	defaultXUsername        = "YozakuraYuura"
	defaultXPollSeconds     = 300
	minXPollSeconds         = 60
	defaultXMonthlyCap      = 250
	minXMonthlyCap          = 10
	defaultPollSeconds      = 120
	minPollSeconds          = 30
	defaultHeartbeatSeconds = 60
	minHeartbeatSeconds     = 15
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
	CollabFile     string // who people tagged as being in a stream, by hand
	RodokFile      string // how many times each person has used /rodok on each other person
	SettingsFile   string // what /setup saved; it beats .env for channels and roles
	Token          string
	AppID          string
	GuildID        string // optional: found from Channels.Test when empty
	Channels       Channels
	Pings          Pings
	YouTube        YouTube
	X              X
	Review         Review
	HeartbeatEvery time.Duration

	// Written is what .env says for the things /setup can change, exactly as written (no defaults filled in). The settings package adds its own defaults and the saved /setup values on top.
	Written settings.Env
}

// Load validates every setting and reports all problems at once, so you fix the .env file in one go instead of one error per run.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	get := func(key string) string { return strings.TrimSpace(getenv(key)) }

	var c Config

	c.Token = get("DISCORD_TOKEN")
	switch {
	case c.Token == "":
		problems = append(problems, "DISCORD_TOKEN is empty (Developer Portal > Bot > Reset Token)")
	case strings.HasPrefix(strings.ToLower(c.Token), "bot "):
		problems = append(problems, `DISCORD_TOKEN must be the bare token, without the word "Bot"`)
	}

	requireID := func(key string) string {
		v := get(key)
		if !snowflake.MatchString(v) {
			problems = append(problems, key+" must be a Discord ID (17-20 digits), got "+quote(v))
		}
		return v
	}
	optionalID := func(key, fallback string) string {
		v := get(key)
		if v == "" {
			return fallback
		}
		if !snowflake.MatchString(v) {
			problems = append(problems, key+" must be a Discord ID (17-20 digits), got "+quote(v))
		}
		return v
	}

	c.AppID = requireID("DISCORD_APP_ID")
	c.Channels.Test = requireID("CHANNEL_TEST")
	c.GuildID = optionalID("GUILD_ID", "")
	c.Channels.Ops = optionalID("CHANNEL_OPS", c.Channels.Test)
	c.Channels.Announce = optionalID("CHANNEL_ANNOUNCE", c.Channels.Test)
	c.Channels.Approval = optionalID("CHANNEL_APPROVAL", c.Channels.Test)
	c.Channels.Recap = optionalID("CHANNEL_RECAP", c.Channels.Announce)
	c.Channels.Tweets = optionalID("CHANNEL_TWEETS", c.Channels.Announce)
	c.Channels.CollabLinks = optionalID("CHANNEL_COLLAB_LINKS", "")
	c.Channels.Reminder = optionalID("CHANNEL_REMINDER", "")
	c.Pings.Upcoming = optionalID("ROLE_PING_UPCOMING", "")
	c.Pings.Live = optionalID("ROLE_PING_LIVE", "")
	c.Pings.Both = optionalID("ROLE_PING", "")
	c.Pings.Tweets = optionalID("ROLE_PING_TWEETS", "")

	// The key is never repeated in an error message, whatever is wrong with it.
	c.YouTube.APIKey = get("YOUTUBE_API_KEY")
	if v := get("YOUTUBE_CHANNEL_ID"); v != "" {
		if !youtubeChannelID.MatchString(v) {
			problems = append(problems, "YOUTUBE_CHANNEL_ID must look like UC followed by 22 characters, got "+quote(v))
		}
		c.YouTube.ChannelID = v
	}

	pollSecs := defaultPollSeconds
	if v := get("YOUTUBE_POLL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minPollSeconds {
			problems = append(problems, "YOUTUBE_POLL_SECONDS must be a whole number of "+strconv.Itoa(minPollSeconds)+" or more, got "+quote(v))
		} else {
			pollSecs = n
		}
	}
	c.YouTube.PollEvery = time.Duration(pollSecs) * time.Second

	c.X = loadX(get, &problems)

	c.PartnersFile = get("PARTNERS_FILE")
	if c.PartnersFile == "" {
		c.PartnersFile = defaultPartnersFile
	}

	c.Review.RoleID = optionalID("ROLE_COLLAB_REVIEW", "")
	c.Written = settings.Env{
		Values: map[settings.Key]string{
			settings.Announce:  get("CHANNEL_ANNOUNCE"),
			settings.Tweets:    get("CHANNEL_TWEETS"),
			settings.Recap:     get("CHANNEL_RECAP"),
			settings.Fans:      get("CHANNEL_COLLAB_LINKS"),
			settings.Review:    get("CHANNEL_APPROVAL"),
			settings.Ops:       get("CHANNEL_OPS"),
			settings.Staff:     get("CHANNEL_TEST"),
			settings.Reminder:  get("CHANNEL_REMINDER"),
			settings.Ping:      get("ROLE_PING"),
			settings.TweetPing: get("ROLE_PING_TWEETS"),
			settings.Reviewer:  get("ROLE_COLLAB_REVIEW"),
		},
		PingUpcoming: c.Pings.Upcoming,
		PingLive:     c.Pings.Live,
	}
	c.Review.File = get("OUTSIDE_FILE")
	if c.Review.File == "" {
		c.Review.File = defaultOutsideFile
	}
	words := get("COLLAB_MENTION_WORDS")
	if words == "" {
		words = defaultMentionWords
	}
	for _, w := range strings.Split(words, ",") {
		if w = strings.TrimSpace(w); w != "" {
			c.Review.MentionWords = append(c.Review.MentionWords, w)
		}
	}
	for _, id := range strings.Split(get("COLLAB_IGNORE_CHANNELS"), ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		} else if !youtubeChannelID.MatchString(id) {
			problems = append(problems, "COLLAB_IGNORE_CHANNELS must be YouTube channel IDs (UC followed by 22 characters), got "+quote(id))
		} else {
			c.Review.IgnoreChannels = append(c.Review.IgnoreChannels, id)
		}
	}

	c.CollabFile = get("COLLAB_FILE")
	if c.CollabFile == "" {
		c.CollabFile = defaultCollabFile
	}

	c.SettingsFile = get("SETTINGS_FILE")
	if c.SettingsFile == "" {
		c.SettingsFile = defaultSettingsFile
	}

	c.RodokFile = get("RODOK_FILE")
	if c.RodokFile == "" {
		c.RodokFile = defaultRodokFile
	}

	c.HistoryFile = get("HISTORY_FILE")
	if c.HistoryFile == "" {
		c.HistoryFile = defaultHistoryFile
	}

	c.DataFile = get("DATA_FILE")
	if c.DataFile == "" {
		c.DataFile = defaultDataFile
	}

	secs := defaultHeartbeatSeconds
	if v := get("HEARTBEAT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minHeartbeatSeconds {
			problems = append(problems, "HEARTBEAT_SECONDS must be a whole number of "+strconv.Itoa(minHeartbeatSeconds)+" or more, got "+quote(v))
		} else {
			secs = n
		}
	}
	c.HeartbeatEvery = time.Duration(secs) * time.Second

	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return c, nil
}

// An X handle is 1 to 15 letters, digits or underscores.
var xUsername = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// RelayKinds are the post types the relay can pass on, in the order they are listed by default.
var RelayKinds = []string{"post", "reply", "quote", "repost"}

// loadX reads the X settings. The token is never repeated in an error message.
func loadX(get func(string) string, problems *[]string) X {
	x := X{BearerToken: get("X_BEARER_TOKEN"), Username: defaultXUsername, MonthlyCap: defaultXMonthlyCap, FanLookupCap: defaultXFanLookupCap}

	if v := strings.TrimPrefix(get("X_USERNAME"), "@"); v != "" {
		if !xUsername.MatchString(v) {
			*problems = append(*problems, "X_USERNAME must be an X handle (letters, digits and _, up to 15), got "+quote(v))
		} else {
			x.Username = v
		}
	}

	secs := defaultXPollSeconds
	if v := get("X_POLL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minXPollSeconds {
			*problems = append(*problems, "X_POLL_SECONDS must be a whole number of "+strconv.Itoa(minXPollSeconds)+" or more, got "+quote(v))
		} else {
			secs = n
		}
	}
	x.PollEvery = time.Duration(secs) * time.Second

	if v := get("X_MONTHLY_READ_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minXMonthlyCap {
			*problems = append(*problems, "X_MONTHLY_READ_CAP must be a whole number of "+strconv.Itoa(minXMonthlyCap)+" or more, got "+quote(v))
		} else {
			x.MonthlyCap = n
		}
	}

	if v := get("X_FAN_LOOKUP_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			*problems = append(*problems, "X_FAN_LOOKUP_CAP must be a whole number, 0 or more, got "+quote(v))
		} else {
			x.FanLookupCap = n
		}
	}

	x.Kinds = RelayKinds
	if v := get("X_RELAY_TYPES"); v != "" {
		x.Kinds = nil
		for _, k := range strings.Split(v, ",") {
			k = strings.ToLower(strings.TrimSpace(k))
			switch {
			case k == "":
			case !slices.Contains(RelayKinds, k):
				*problems = append(*problems, "X_RELAY_TYPES has an unknown type "+quote(k)+" (use post, reply, quote, repost)")
			case !slices.Contains(x.Kinds, k):
				x.Kinds = append(x.Kinds, k)
			}
		}
		if len(x.Kinds) == 0 && len(*problems) == 0 {
			*problems = append(*problems, "X_RELAY_TYPES is empty; list at least one of post, reply, quote, repost")
		}
	}
	return x
}

func quote(s string) string {
	if s == "" {
		return "nothing"
	}
	return strconv.Quote(s)
}
