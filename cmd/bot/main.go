// Command bot is the fancord Discord bot.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/catchup"
	"github.com/Nadif243/fancord-bot/internal/collabs"
	"github.com/Nadif243/fancord-bot/internal/config"
	"github.com/Nadif243/fancord-bot/internal/discord"
	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/ops"
	"github.com/Nadif243/fancord-bot/internal/outside"
	"github.com/Nadif243/fancord-bot/internal/partners"
	"github.com/Nadif243/fancord-bot/internal/poller"
	"github.com/Nadif243/fancord-bot/internal/recap"
	"github.com/Nadif243/fancord-bot/internal/relay"
	"github.com/Nadif243/fancord-bot/internal/rodok"
	"github.com/Nadif243/fancord-bot/internal/settings"
	"github.com/Nadif243/fancord-bot/internal/store"
	"github.com/Nadif243/fancord-bot/internal/xapi"
	"github.com/Nadif243/fancord-bot/internal/youtube"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=0.2.0" ./cmd/bot
var version = "0.1.0-dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("bot stopped with an error", "err", err)
		os.Exit(1)
	}
}

// loadPartners reads the collab partner list. No file is fine (the recap then has no collab line, and /collab has no names to offer); a file with mistakes stops the bot, with every mistake listed, because quietly ignoring it would hide partners from the recap.
// The result is nil when there is no list; a nil *partners.Book is a valid, empty one.
func loadPartners(path string, log *slog.Logger) (*partners.Book, error) {
	book, err := partners.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Info("no partners file: the recap will not list collab partners", "path", path)
		return nil, nil
	case err != nil:
		return nil, err
	}
	log.Info("partners file ready", "path", path, "partners", book.Len())
	return book, nil
}

// Gateway intents beyond the default. They are written as numbers (Discord's own bit values) rather than by the library's names.
const (
	intentGuildMessages  = discordgo.Intent(1 << 9)
	intentMessageContent = discordgo.Intent(1 << 15) // privileged
)

func run(log *slog.Logger) error {
	// A missing .env is fine (the settings may come from the real environment).
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .env: %w", err)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	s, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuilds // no privileged intents needed yet

	messenger := discord.SessionMessenger{S: s}

	memory, err := store.Open(cfg.DataFile)
	if err != nil {
		return err
	}
	log.Info("memory file ready", "path", cfg.DataFile)
	streams, err := history.Open(cfg.HistoryFile)
	if err != nil {
		return err
	}
	log.Info("history file ready", "path", cfg.HistoryFile)
	rodokCounts, err := rodok.OpenFile(cfg.RodokFile)
	if err != nil {
		return err
	}
	log.Info("rodok file ready", "path", cfg.RodokFile)

	// The channels and roles the bot uses. What /setup saved beats .env, and everything below asks this each time it posts, so a change applies at once.
	live, err := settings.Open(cfg.SettingsFile, cfg.Written)
	if err != nil {
		return err
	}
	log.Info("settings file ready", "path", cfg.SettingsFile)
	channel := func(k settings.Key) func() string { return func() string { return live.Get(k) } }

	announcer := broadcast.NewAnnouncer(messenger, cfg.Channels.Announce, memory).
		WithChannel(channel(settings.Announce)).
		WithPingFuncs(live.PingUpcoming, live.PingLive)

	// /devstream gets its own announcer whose memory is only RAM, so fake streams never end up in the state file next to the real ones.
	devAnnouncer := broadcast.NewAnnouncer(messenger, cfg.Channels.Test, broadcast.NewMapMemory()).
		WithChannel(channel(settings.Staff))
	// No ping roles: /devstream must never tag the real role.

	// Stays nil without a YouTube key; /devcheck then says YouTube is not set up.
	var source discord.StreamSource
	var yt *youtube.Client
	if cfg.YouTube.APIKey != "" {
		yt = youtube.NewClient(cfg.YouTube.APIKey)
		source = yt
	}

	// The recap reads the history, so it only makes sense while the poller
	// is filling it.
	pollerOn := source != nil && cfg.YouTube.ChannelID != ""
	var recapService *recap.Service
	var collab discord.CollabTagger // stays a nil interface when the recap is off
	var review *outside.Service     // collabs on other channels; nil when YouTube is off
	if pollerOn {
		book, err := loadPartners(cfg.PartnersFile, log)
		if err != nil {
			return err
		}
		tags, err := collabs.OpenFile(cfg.CollabFile)
		if err != nil {
			return err
		}
		log.Info("collab tags file ready", "path", cfg.CollabFile)

		// Who was in a stream = named in its title + tagged by hand - removed by hand.
		// The recap and /collab both go through this one service.
		who := collabs.NewService(book, tags, streams)
		recapService = recap.New(streams, messenger, memory, cfg.Channels.Recap, cfg.YouTube.PollEvery+time.Minute, log).
			WithPartners(who, cfg.Channels.Ops).
			WithReminder(cfg.Channels.Test, memory).
			WithLiveChannels(channel(settings.Recap), channel(settings.Ops), channel(settings.Reminder))
		collab = who

		decisions, err := outside.OpenFile(cfg.Review.File)
		if err != nil {
			return err
		}
		log.Info("collab review file ready", "path", cfg.Review.File)
		review = outside.New(decisions, yt, messenger, book, outside.Settings{
			ChannelID:      cfg.Channels.Approval,
			ChannelFn:      channel(settings.Review),
			OwnChannelID:   cfg.YouTube.ChannelID,
			MentionWords:   cfg.Review.MentionWords,
			IgnoreChannels: cfg.Review.IgnoreChannels,
		}, log).WithAnnouncer(announcer)
		if cfg.X.BearerToken != "" && cfg.X.FanLookupCap > 0 {
			// Its own client: the relay's runs in another goroutine.
			review.WithX(xapi.NewClient(cfg.X.BearerToken), cfg.X.FanLookupCap)
		}
		recapService.WithGuests(review) // confirmed collabs on other channels join the weekly recap
	}
	var reviewer discord.CollabReviewer // stays a nil interface when review is off
	if review != nil {
		reviewer = review
	}
	var preview discord.RecapPreviewer          // stays a nil interface when the recap is off
	var remindPreview discord.ReminderPreviewer // same for the reminder
	if recapService != nil {
		preview = recapService
		remindPreview = recapService
	}

	listening := reviewer != nil && live.Get(settings.Fans) != ""
	router := discord.NewRouter(log, discord.Deps{
		DevAnnouncer:     devAnnouncer,
		DevChannelID:     cfg.Channels.Test,
		Source:           source,
		YouTubeChannelID: cfg.YouTube.ChannelID,
		Recap:            preview,
		Reminder:         remindPreview,
		Collab:           collab,
		Outside:          reviewer,
		Rodok:            rodok.New(rodokCounts),
		DevChannel:       channel(settings.Staff),
		LinkChannel:      channel(settings.Fans),
		ReviewRole:       channel(settings.Reviewer),
		Settings:         live,
		// Reading what people type needs the Message Content intent, which must also be switched on in the Developer Portal (Bot -> Privileged Gateway Intents). Asking for it without that switch makes Discord refuse the connection, so it is asked for only when a fan channel is set at start. A channel set later by /setup then needs one restart.
		ListeningForMessages: listening,
	})
	s.AddHandler(router.OnInteraction)
	if reviewer != nil {
		s.AddHandler(router.OnMessage) // it ignores every channel but the fan channel
	}
	if listening {
		s.Identify.Intents |= intentGuildMessages | intentMessageContent
		log.Info("fan collab-link channel is on", "channel", live.Get(settings.Fans))
	} else if live.Get(settings.Fans) != "" {
		log.Warn("a fan collab-link channel is set, but collab review is off (it needs YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID); the channel is ignored")
	}
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		log.Info("connected to Discord", "bot", r.User.Username)
	})

	if err := s.Open(); err != nil {
		return fmt.Errorf("connect to Discord: %w", err)
	}
	defer s.Close()

	// Find the server from the test channel in .env (never a /setup change, so a deleted channel cannot stop the bot from starting), unless GUILD_ID was given.
	guildID := cfg.GuildID
	if guildID == "" {
		ch, err := s.Channel(cfg.Channels.Test)
		if err != nil {
			return fmt.Errorf("cannot see the test channel (is the bot invited, and allowed to view the bot-dev category?): %w", err)
		}
		guildID = ch.GuildID
	}
	if err := router.Register(s, cfg.AppID, guildID); err != nil {
		return err
	}
	log.Info("slash commands registered", "guild", guildID)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Everything that runs in the background goes through start, so shutdown can wait for all of it with one wg.Wait().
	var wg sync.WaitGroup
	start := func(run func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(ctx)
		}()
	}

	// The tweet relay needs only X and Discord, not the YouTube poller.
	if cfg.X.BearerToken != "" {
		kinds := make([]xapi.Kind, len(cfg.X.Kinds))
		for i, k := range cfg.X.Kinds {
			kinds[i] = xapi.Kind(k)
		}
		rel := relay.New(xapi.NewClient(cfg.X.BearerToken), memory, messenger, relay.Settings{
			Username:   cfg.X.Username,
			ChannelID:  cfg.Channels.Tweets,
			OpsID:      cfg.Channels.Ops,
			ChannelFn:  channel(settings.Tweets),
			OpsFn:      channel(settings.Ops),
			RoleFn:     channel(settings.TweetPing),
			Every:      cfg.X.PollEvery,
			Kinds:      kinds,
			MonthlyCap: cfg.X.MonthlyCap,
		}, log)
		if review != nil {
			// YouTube links in her posts become collab candidates. Links the
			// relay saw before this feature existed are picked up too.
			rel.OnLinks(func(postID string, videoIDs []string) { review.Enqueue("tweet", postID, videoIDs...) })
			review.Seed(memory.Relay().Links)
		}
		start(rel.Run)
	} else {
		log.Info("tweet relay is off: set X_BEARER_TOKEN to turn it on")
	}

	start(ops.NewHeartbeat(messenger, cfg.Channels.Ops, version, cfg.HeartbeatEvery, log).
		WithChannel(channel(settings.Ops)).Run)

	if pollerOn {
		// Writes down "still running" every few minutes; after the first YouTube check of a start it tells the ops channel what happened while off.
		back := catchup.New(memory, streams, announcer, messenger, cfg.Channels.Ops, log).
			WithOps(channel(settings.Ops))
		start(back.Run)
		start(poller.New(source, announcer, streams, cfg.YouTube.ChannelID, cfg.YouTube.PollEvery, log).
			OnFirstCheck(back.FirstCheckDone).Run)
		start(recapService.Run)
		start(review.Run)
	} else {
		log.Info("youtube polling is off: set YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID to turn it on")
	}

	log.Info("bot is running; press Ctrl+C to stop", "version", version)
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait() // let the heartbeat mark itself Offline before the connection closes
	return nil
}
