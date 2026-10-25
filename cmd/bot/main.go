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
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/config"
	"github.com/Nadif243/fancord-bot/internal/discord"
	"github.com/Nadif243/fancord-bot/internal/ops"
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
	announcer := broadcast.NewAnnouncer(messenger, cfg.Channels.Announce)
	router := discord.NewRouter(log, announcer, cfg.Channels.Test)
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

	hb := ops.NewHeartbeat(messenger, cfg.Channels.Ops, version, cfg.HeartbeatEvery, log)
	hbDone := make(chan struct{})
	go func() {
		hb.Run(ctx)
		close(hbDone)
	}()

	log.Info("bot is running; press Ctrl+C to stop", "version", version)
	<-ctx.Done()
	log.Info("shutting down")
	<-hbDone // let the heartbeat mark itself Offline before the connection closes
	return nil
}
