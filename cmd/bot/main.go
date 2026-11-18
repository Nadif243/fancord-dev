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
	"github.com/Nadif243/fancord-bot/internal/rolepanel"
	"github.com/Nadif243/fancord-bot/internal/settings"
	"github.com/Nadif243/fancord-bot/internal/store"
	"github.com/Nadif243/fancord-bot/internal/translate"
	"github.com/Nadif243/fancord-bot/internal/which"
	"github.com/Nadif243/fancord-bot/internal/xapi"
	"github.com/Nadif243/fancord-bot/internal/youtube"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=0.2.0" ./cmd/bot
var version = "0.1.0-dev"

// The gateway intents the bot asks for besides Guilds, written as Discord's own bit numbers (see its "Gateway Intents" table) rather than by the library's names.
const (
	intentGuildMessages  = discordgo.Intent(1 << 9)
	intentMessageContent = discordgo.Intent(1 << 15) // privileged
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("bot stopped with an error", "err", err)
		os.Exit(1)
	}
}

// run builds everything, connects to Discord, starts the background jobs and waits for the signal to stop. Building comes first and the pieces are made in the helper functions below, so a mistake in a file or a setting stops the bot before it connects.
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
	s.Identify.Intents = discordgo.IntentsGuilds // the privileged one is added below, only when needed
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

	announcer := broadcast.NewAnnouncer(messenger, cfg.Channels.Announce, memory).
		WithChannel(live.Getter(settings.Announce)).
		WithPingFuncs(live.PingUpcoming, live.PingLive).
		WithVideoRole(live.PingLive) // a new video or Short pings the same role as a live stream

	// /devstream gets its own announcer whose memory is only RAM, so fake streams never end up in the state file next to the real ones.
	// It has no ping roles: /devstream must never tag the real role.
	devAnnouncer := broadcast.NewAnnouncer(messenger, cfg.Channels.Test, broadcast.NewMapMemory()).
		WithChannel(live.Getter(settings.Staff))

	// Stays nil without a YouTube key; /devcheck then says YouTube is not set up.
	var source discord.StreamSource
	var yt *youtube.Client
	if cfg.YouTube.APIKey != "" {
		yt = youtube.NewClient(cfg.YouTube.APIKey)
		source = yt
	}

	// Everything that reads her YouTube channel. The recap reads the history, so it only makes sense while the poller is filling it.
	var yf *youtubeFeatures
	if yt != nil && cfg.YouTube.ChannelID != "" {
		yf, err = buildYouTubeFeatures(cfg, log, yt, streams, memory, messenger, live, announcer)
		if err != nil {
			return err
		}
	}

	rel := buildRelay(cfg, log, memory, messenger, live, yf)
	community, err := buildCommunity(cfg, log, yt, messenger, live)
	if err != nil {
		return err
	}

	// The role panel needs no YouTube; it only reads and writes its own file.
	panelFile, err := rolepanel.OpenStore(cfg.RolesFile, rolepanel.Seed())
	if err != nil {
		return err
	}
	log.Info("role panel file ready", "path", cfg.RolesFile)
	panel := discord.PanelAdapter{S: s}

	// Reading what people type needs the Message Content intent, which must also be switched on in the Developer Portal (Bot -> Privileged Gateway Intents).
	// Asking for it without that switch makes Discord refuse the connection, so it is asked for only when a fan channel is set at start. A channel set later by /setup then needs one restart.
	listening := yf != nil && live.Get(settings.Fans) != ""

	deps := discord.Deps{
		DevAnnouncer:         devAnnouncer,
		DevChannelID:         cfg.Channels.Test,
		Source:               source,
		YouTubeChannelID:     cfg.YouTube.ChannelID,
		Rodok:                rodok.New(rodokCounts),
		Roles:                rolepanel.New(panelFile, panel, panel),
		DevChannel:           live.Getter(settings.Staff),
		LinkChannel:          live.Getter(settings.Fans),
		ReviewRole:           live.Getter(settings.Reviewer),
		Settings:             live,
		RodokProtectedRole:   cfg.RodokProtectedRole, // members of this role cannot be rodok-ed; empty means nobody is protected
		ListeningForMessages: listening,
	}
	yf.fill(&deps)
	router := discord.NewRouter(log, deps)

	s.AddHandler(router.OnInteraction)
	if yf != nil {
		s.AddHandler(router.OnMessage) // it ignores every channel but the fan channel
	}
	switch {
	case listening:
		s.Identify.Intents |= intentGuildMessages | intentMessageContent
		log.Info("fan collab-link channel is on", "channel", live.Get(settings.Fans))
	case live.Get(settings.Fans) != "":
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

	if rel != nil {
		start(rel.Run)
	}
	start(ops.NewHeartbeat(messenger, cfg.Channels.Ops, version, cfg.HeartbeatEvery, log).
		WithChannel(live.Getter(settings.Ops)).Run)
	if yf != nil {
		for _, job := range yf.jobs {
			start(job)
		}
	} else {
		log.Info("youtube polling is off: set YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID to turn it on")
	}
	if community != nil {
		start(community)
	}

	log.Info("bot is running; press Ctrl+C to stop", "version", version)
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait() // let the heartbeat mark itself Offline before the connection closes
	return nil
}

// youtubeFeatures is everything that depends on her YouTube channel being set up: the recap, collab tags, nickname questions and the review of collabs on other channels. A nil *youtubeFeatures means they are all off.
type youtubeFeatures struct {
	recap  *recap.Service
	who    *collabs.Service // who was in a stream: named in the title, plus tags
	review *outside.Service
	asks   *which.Service

	// jobs are what runs in the background, in the order they are started.
	jobs []func(context.Context)
}

// buildYouTubeFeatures opens the files these features keep and puts them together, along with the jobs that keep them going.
func buildYouTubeFeatures(
	cfg config.Config, log *slog.Logger, yt *youtube.Client,
	streams *history.File, memory *store.File, messenger discord.SessionMessenger,
	live *settings.Live, announcer *broadcast.Announcer,
) (*youtubeFeatures, error) {
	book, err := loadPartners(cfg.PartnersFile, log)
	if err != nil {
		return nil, err
	}
	tags, err := collabs.OpenFile(cfg.CollabFile)
	if err != nil {
		return nil, err
	}
	log.Info("collab tags file ready", "path", cfg.CollabFile)

	// Who was in a stream = named in its title + tagged by hand - removed by hand.
	// The recap and /collab both go through this one service.
	who := collabs.NewService(book, tags, streams)

	// The recap waits a little longer than one poll for a stream that just ended to be seen as ended.
	settle := cfg.YouTube.PollEvery + time.Minute
	rc := recap.New(streams, messenger, memory, cfg.Channels.Recap, settle, log).
		WithPartners(who, cfg.Channels.Ops).
		WithReminder(cfg.Channels.Test, memory).
		WithLiveChannels(live.Getter(settings.Recap), live.Getter(settings.Ops), live.Getter(settings.Reminder))

	questions, err := which.OpenFile(cfg.WhichFile)
	if err != nil {
		return nil, err
	}
	log.Info("nickname questions file ready", "path", cfg.WhichFile)
	asks := which.New(questions, streams, who, tags, messenger, which.Settings{ChannelFn: live.Getter(settings.Review)}, log)

	decisions, err := outside.OpenFile(cfg.Review.File)
	if err != nil {
		return nil, err
	}
	log.Info("collab review file ready", "path", cfg.Review.File)
	review := outside.New(decisions, yt, messenger, book, outside.Settings{
		ChannelID:      cfg.Channels.Approval,
		ChannelFn:      live.Getter(settings.Review),
		OwnChannelID:   cfg.YouTube.ChannelID,
		MentionWords:   cfg.Review.MentionWords,
		IgnoreChannels: cfg.Review.IgnoreChannels,
	}, log).WithAnnouncer(announcer)
	if cfg.X.BearerToken != "" && cfg.X.FanLookupCap > 0 {
		// Its own client: the relay's runs in another goroutine.
		review.WithX(xapi.NewClient(cfg.X.BearerToken), cfg.X.FanLookupCap)
	}
	rc.WithGuests(review) // confirmed collabs on other channels join the weekly recap

	// Writes down "still running" every few minutes; after the first YouTube check of a start it tells the ops channel what happened while the bot was off.
	back := catchup.New(memory, streams, announcer, messenger, cfg.Channels.Ops, log).
		WithOps(live.Getter(settings.Ops))
	// Her new videos and Shorts are announced and kept in the history (marked as such, so the recap ignores them); her premieres count as streams.
	poll := poller.New(youtube.ActivitySource{C: yt}, announcer, streams, cfg.YouTube.ChannelID, cfg.YouTube.PollEvery, log).
		WithPremieres().OnFirstCheck(back.FirstCheckDone)

	return &youtubeFeatures{
		recap: rc, who: who, review: review, asks: asks,
		jobs: []func(context.Context){back.Run, poll.Run, rc.Run, review.Run, asks.Run},
	}, nil
}

// fill hands the features to the slash commands. The fields stay nil (so each command says it is not set up) when the features are off.
func (f *youtubeFeatures) fill(d *discord.Deps) {
	if f == nil {
		return
	}
	d.Recap, d.Reminder = f.recap, f.recap
	d.Collab = f.who
	d.Outside = f.review
	d.Which = f.asks
}

// buildRelay makes the tweet relay, or returns nil when it is off. It needs only X and Discord, not the YouTube poller; yf (may be nil) gets the YouTube links found in her posts as collab candidates.
func buildRelay(
	cfg config.Config, log *slog.Logger, memory *store.File,
	messenger discord.SessionMessenger, live *settings.Live, yf *youtubeFeatures,
) *relay.Relay {
	if cfg.X.BearerToken == "" {
		log.Info("tweet relay is off: set X_BEARER_TOKEN to turn it on")
		return nil
	}
	kinds := make([]xapi.Kind, len(cfg.X.Kinds))
	for i, k := range cfg.X.Kinds {
		kinds[i] = xapi.Kind(k)
	}
	// Japanese posts get a translation in their card, when a Gemini key is set.
	var translator relay.Translator
	if cfg.Translate.APIKey != "" {
		translator = translate.NewGemini(cfg.Translate.APIKey, cfg.Translate.Model, cfg.Translate.To)
	} else {
		log.Info("tweet translation is off: set GEMINI_API_KEY and GEMINI_MODEL to turn it on")
	}
	rel := relay.New(xapi.NewClient(cfg.X.BearerToken), memory, messenger, relay.Settings{
		Username:   cfg.X.Username,
		ChannelID:  cfg.Channels.Tweets,
		OpsID:      cfg.Channels.Ops,
		ChannelFn:  live.Getter(settings.Tweets),
		OpsFn:      live.Getter(settings.Ops),
		RoleFn:     live.Getter(settings.TweetPing),
		Every:      cfg.X.PollEvery,
		Kinds:      kinds,
		MonthlyCap: cfg.X.MonthlyCap,
		Cards:      cfg.X.Style == "card",
		Translator: translator,
	}, log)
	if yf != nil {
		// YouTube links in her posts become collab candidates. Links the relay saw before this feature existed are picked up too.
		rel.OnLinks(func(postID string, videoIDs []string) { yf.review.Enqueue("tweet", postID, videoIDs...) })
		yf.review.Seed(memory.Relay().Links)
	}
	return rel
}

// buildCommunity makes the job for the community's channel, or returns nil when it is not set up. The channel has its own memory file, announcer and poller, and no history, so it never reaches the recap or the collab features.
func buildCommunity(
	cfg config.Config, log *slog.Logger, yt *youtube.Client,
	messenger discord.SessionMessenger, live *settings.Live,
) (func(context.Context), error) {
	c := cfg.Community
	switch {
	case c.ChannelID == "":
		return nil, nil // not set up
	case yt == nil:
		log.Warn("the community channel is set, but YOUTUBE_API_KEY is empty; it is ignored")
		return nil, nil
	}

	memory, err := store.Open(c.File)
	if err != nil {
		return nil, err
	}
	log.Info("community file ready", "path", c.File, "channel", c.Name)

	// Its own destination, or the announcement channel when none is set.
	where := live.Getter(settings.Announce)
	if c.Channel != "" {
		where = func() string { return c.Channel }
	}
	// Its role: ROLE_PING_COMMUNITY when set, else the very same roles as hers (so a change made with /setup applies here too).
	upcomingRole, liveRole := live.PingUpcoming, live.PingLive
	if c.Role != "" {
		own := func() string { return c.Role }
		upcomingRole, liveRole = own, own
	}
	ann := broadcast.NewAnnouncer(messenger, cfg.Channels.Announce, memory).
		WithChannel(where).
		WithPingFuncs(upcomingRole, liveRole).
		WithVideoRole(liveRole).
		WithCommunity(c.Name)
	return poller.New(youtube.ActivitySource{C: yt}, ann, nil, c.ChannelID, c.PollEvery,
		log.With("channel", "community")).WithPremieres().Run, nil
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
