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
	"github.com/Nadif243/fancord-bot/internal/config"
	"github.com/Nadif243/fancord-bot/internal/discord"
	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/ops"
	"github.com/Nadif243/fancord-bot/internal/partners"
	"github.com/Nadif243/fancord-bot/internal/poller"
	"github.com/Nadif243/fancord-bot/internal/recap"
	"github.com/Nadif243/fancord-bot/internal/store"
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

// loadPartners reads the collab partner list. No file is fine (the recap then has no collab line); a file with mistakes stops the bot, with every mistake listed, because quietly ignoring it would hide partners from the recap.
// The result is nil when there is no list, which recap.WithPartners accepts.
func loadPartners(path string, log *slog.Logger) (recap.Matcher, error) {
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
	announcer := broadcast.NewAnnouncer(messenger, cfg.Channels.Announce, memory).
		WithPings(cfg.Pings.Upcoming, cfg.Pings.Live)

	// /devstream gets its own announcer whose memory is only RAM, so fake streams never end up in the state file next to the real ones.
	devAnnouncer := broadcast.NewAnnouncer(messenger, cfg.Channels.Test, broadcast.NewMapMemory()).
		WithPings(cfg.Pings.Upcoming, cfg.Pings.Live)

	// Stays nil without a YouTube key; /devcheck then says YouTube is not set up.
	var source discord.StreamSource
	if cfg.YouTube.APIKey != "" {
		source = youtube.NewClient(cfg.YouTube.APIKey)
	}

	// The recap reads the history, so it only makes sense while the poller
	// is filling it.
	pollerOn := source != nil && cfg.YouTube.ChannelID != ""
	var recapService *recap.Service
	if pollerOn {
		book, err := loadPartners(cfg.PartnersFile, log)
		if err != nil {
			return err
		}
		recapService = recap.New(streams, messenger, memory, cfg.Channels.Recap, cfg.YouTube.PollEvery+time.Minute, log).
			WithPartners(book, cfg.Channels.Ops)
	}
	var preview discord.RecapPreviewer // stays a nil interface when the recap is off
	if recapService != nil {
		preview = recapService
	}

	router := discord.NewRouter(log, discord.Deps{
		DevAnnouncer:     devAnnouncer,
		DevChannelID:     cfg.Channels.Test,
		Source:           source,
		YouTubeChannelID: cfg.YouTube.ChannelID,
		Recap:            preview,
	})
	s.AddHandler(router.OnInteraction)
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		log.Info("connected to Discord", "bot", r.User.Username)
	})

	if err := s.Open(); err != nil {
		return fmt.Errorf("connect to Discord: %w", err)
	}
	defer s.Close()

	// Find the server from the test channel, unless GUILD_ID was given.
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

	start(ops.NewHeartbeat(messenger, cfg.Channels.Ops, version, cfg.HeartbeatEvery, log).Run)

	if pollerOn {
		start(poller.New(source, announcer, streams, cfg.YouTube.ChannelID, cfg.YouTube.PollEvery, log).Run)
		start(recapService.Run)
	} else {
		log.Info("youtube polling is off: set YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID to turn it on")
	}

	log.Info("bot is running; press Ctrl+C to stop", "version", version)
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait() // let the heartbeat mark itself Offline before the connection closes
	return nil
}
