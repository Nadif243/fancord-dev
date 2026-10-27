// Package config reads the bot's settings from environment variables.
//
// Load takes the lookup function as an argument (os.Getenv in production, a map lookup in tests), so it never touches the real environment itself.
package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A Discord ID ("snowflake") is a number of 17 to 20 digits.
var snowflake = regexp.MustCompile(`^\d{17,20}$`)

// A YouTube channel ID is "UC" followed by 22 letters, digits, "-" or "_".
var youtubeChannelID = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

const (
	defaultDataFile         = "data/state.json"
	defaultHistoryFile      = "data/history.json"
	defaultPartnersFile     = "partners.csv"
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
}

type Config struct {
	DataFile       string // where the bot remembers what it announced
	HistoryFile    string // where it records every stream it sees, for the recap
	PartnersFile   string // the collab partner list (CSV) the recap reads
	Token          string
	AppID          string
	GuildID        string // optional: found from Channels.Test when empty
	Channels       Channels
	Pings          Pings
	YouTube        YouTube
	HeartbeatEvery time.Duration
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
	c.Pings.Upcoming = optionalID("ROLE_PING_UPCOMING", "")
	c.Pings.Live = optionalID("ROLE_PING_LIVE", "")

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

	c.PartnersFile = get("PARTNERS_FILE")
	if c.PartnersFile == "" {
		c.PartnersFile = defaultPartnersFile
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

func quote(s string) string {
	if s == "" {
		return "nothing"
	}
	return strconv.Quote(s)
}
