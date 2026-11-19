# fancord-bot

A Discord bot, written in Go, for the fancord of the VTuber **Yuura Yozakura** (Livium Project). It announces her streams and uploads, relays her X posts as cards (with a Japanese translation), posts a weekly stream recap with the collab partners, hands out roles through a button panel, and keeps a few small community features.

- Language: Go 1.24, [discordgo](https://github.com/bwmarrin/discordgo) v0.29
- Module: `github.com/Nadif243/fancord-bot`
- Runs in Docker (see [DOCKER.md](DOCKER.md)); everything it remembers lives in `data/`
- All the bot's messages are in Indonesian (they are in `internal/msg`); admin commands answer in English

---

## What it does

### YouTube announcements
- Checks Yuura's channel every `YOUTUBE_POLL_SECONDS` (default 120) and keeps **one message per stream**, edited as it goes **waiting room → live → ended**.
- Pings a role when a waiting room appears and when a stream goes live.
- **Premieres** are announced like streams. New **videos** and **Shorts** (published in the last 48 hours) get one message, never edited, and ping the same role as a live stream.
- A waiting room that disappears from YouTube is marked as cancelled.
- Collabs on *other* channels (confirmed through the review below) are announced the same way, with the name of the channel.
- After a restart it catches up on what happened while it was off and tells the ops channel.

### The community's own YouTube channel (optional)
- A second, plainer channel: streams, premieres, videos and Shorts are announced in the same channel with the same role, with the channel's name in each message.
- Nothing else: no history, no recap, no collab handling. It keeps its own memory file.

### Weekly recap
- **Every Sunday 20:00 WIB**: a recap of the week's streams (title, date, length) with the collab partners who were in them. If a stream is still live at that time the recap waits for it (at most 3 hours).
- **Saturday 12:00 WIB**: a reminder listing the streams whose collab partners nobody has checked yet, so people can tag them with `/collab`.
- Only live streams are counted. Videos, Shorts and premieres are kept in the history but left out.

### Collab partners
- `partners.csv` lists partners, groups (teams) and words to ignore (see below). A partner is found when their name or one of their spellings appears in a stream title as a whole word.
- A **nickname shared by two partners** (for example "aya") with no other clue makes the bot ask in the review channel, with one button per candidate, plus *Both* and *Neither*. Only members of the reviewer role can answer. Unanswered means nobody is tagged in the recap.
- `/collab` lets people add or remove partners on a stream by hand.

### Collabs on other channels
- Videos linked in her X posts, links fans paste in the fan channel, and links sent with `/collablink` are looked up on YouTube and posted in the review channel with **Confirm / Ignore / Ignore channel** buttons. A confirmed collab is announced and joins the recap.

### X (Twitter) relay
- Relays her posts, replies, quotes and reposts as a **card** (author, caption, pictures or video, quoted post) with an optional ping role. Cards use Discord's Components V2, which is why they can hold video. A post the card cannot show is sent as a plain link.
- Captions written mostly in Japanese get a **Gemini translation** (into Indonesian by default).
- X bills per post read, so there is a monthly cap; the relay pauses when it is reached and the ops channel is warned at 80%.

### Role panel
- Buttons (not reactions) in the roles channel, grouped as embeds. A group is either *pick any* or *pick one*. Pressing a button gives or takes the role and answers privately.
- Edited from Discord with `/rolepanel`; the groups and roles are in `data/roles.json`.
- If a role name already contains an emoji, the panel shows the name without it.
- Roles with powerful permissions (manage server/roles/channels, ban, administrator, ...) are refused.

### Small things
- `/rodok` and `/rodokboard`: a joke counter of who "rodok"-ed whom, with limits against spam.
- A heartbeat message in the ops channel, edited every minute: if it says "last beat 20 minutes ago", the bot is down.

---

## Slash commands

| Command | Who | What it does |
|---|---|---|
| `/ping` | everyone | Checks the bot is alive |
| `/collablink link` | everyone | Sends a YouTube link of a collab on another channel for review |
| `/rodok user` | everyone | Rodok somebody (cooldown 4 s, 12 per 10 min, then a 1-hour lockout) |
| `/rodokboard peringkat` | everyone | Leaderboard of who rodoks / gets rodok-ed the most |
| `/setup show\|channel\|role\|reset` | Manage Server | Changes the channels and roles the bot uses while it runs |
| `/rolepanel show\|add\|remove\|category\|publish` | Manage Server | Sets up and posts the role panel |
| `/collab stream add remove` | staff channel | Tags who was in a stream (autocomplete for all three) |
| `/devstream`, `/devcheck`, `/devrecap`, `/devremind` | staff channel | Test tools: a fake stream, a YouTube check, a recap preview, the Saturday reminder |

"Staff channel" means the channel set as `staff` in `/setup` (`CHANNEL_TEST` by default).

---

## Setup

### 1. Discord
1. Developer Portal → New Application → Bot → **Reset Token** (this is `DISCORD_TOKEN`; never share it).
2. Invite the bot with the `bot` and `applications.commands` scopes and these permissions: View Channel, Send Messages, Embed Links, Manage Roles (for the role panel), and Add Reactions in the fan link channel. The roles the bot pings must allow "anyone to @mention this role".
3. **Message Content intent**: only if you use the fan link channel (`CHANNEL_COLLAB_LINKS`). Switch it on in the Developer Portal (Bot → Privileged Gateway Intents) *before* setting that channel, or the bot cannot connect.
4. For the role panel, put the bot's role **above** the roles it hands out.
5. Turn on Developer Mode in Discord to copy channel and role IDs.

### 2. Keys
- **YouTube Data API v3** key (Google Cloud Console) → `YOUTUBE_API_KEY`
- **X API** bearer token → `X_BEARER_TOKEN` (optional; without it the relay is off)
- **Gemini** key and model → `GEMINI_API_KEY`, `GEMINI_MODEL` (optional; without them nothing is translated)

### 3. Configure
Copy `.env.example` to `.env` and fill it in. Only `DISCORD_TOKEN`, `DISCORD_APP_ID` and
`CHANNEL_TEST` are required; everything else has a default or switches a feature off.
`.env` is never committed.

### 4. Run
```
docker compose up -d --build     # build and start
docker compose logs -f           # watch it
```
Without Docker: `go run ./cmd/bot`.
`data/` holds everything the bot remembers; keep it between rebuilds (DOCKER.md explains how), or the bot will announce things twice and forget the history.

---

## Configuration

`/setup` beats `.env` for channels and roles; `/setup reset` goes back to `.env`.

| Setting | Meaning | Default |
|---|---|---|
| `DISCORD_TOKEN`, `DISCORD_APP_ID`, `CHANNEL_TEST` | Required | |
| `CHANNEL_ANNOUNCE` | Stream and upload announcements | `CHANNEL_TEST` |
| `CHANNEL_OPS` | Heartbeat, alerts, "Back online" | `CHANNEL_TEST` |
| `CHANNEL_APPROVAL` | Collab review and nickname questions | `CHANNEL_TEST` |
| `CHANNEL_RECAP` | Weekly recap | `CHANNEL_ANNOUNCE` |
| `CHANNEL_REMINDER` | Saturday reminder | staff channel |
| `CHANNEL_TWEETS` | Relayed X posts | `CHANNEL_ANNOUNCE` |
| `CHANNEL_COLLAB_LINKS` | Fan channel for collab links (needs the Message Content intent) | off |
| `ROLE_PING_LIVE`, `ROLE_PING_UPCOMING`, `ROLE_PING` | Roles pinged for streams; `ROLE_PING` is one role for both | no ping |
| `ROLE_PING_TWEETS` | Role pinged for each relayed post | no ping |
| `ROLE_COLLAB_REVIEW` | Who may press the review and nickname buttons | Manage Server |
| `YOUTUBE_API_KEY`, `YOUTUBE_CHANNEL_ID` | Her channel (needs both) | off |
| `YOUTUBE_POLL_SECONDS` | How often it is checked (30+) | 120 |
| `COMMUNITY_YOUTUBE_CHANNEL_ID`, `COMMUNITY_NAME` | The community channel (set both) | off |
| `CHANNEL_COMMUNITY`, `ROLE_PING_COMMUNITY` | Its own destination or role | same as hers |
| `COMMUNITY_POLL_SECONDS` | How often it is checked | 300 |
| `X_BEARER_TOKEN`, `X_USERNAME` | The relay and the handle | off, `YozakuraYuura` |
| `X_POLL_SECONDS` | How often X is checked (60+) | 300 |
| `X_RELAY_TYPES` | `post,reply,quote,repost` | all four |
| `X_MONTHLY_READ_CAP` | Billed reads per month before the relay pauses | 250 |
| `X_FAN_LOOKUP_CAP` | X posts per month read for fan links (0 = off) | 20 |
| `TWEET_STYLE` | `card` or `link` | `card` |
| `GEMINI_API_KEY`, `GEMINI_MODEL`, `TRANSLATE_TO` | Translation | off, `Indonesian` |
| `COLLAB_MENTION_WORDS`, `COLLAB_IGNORE_CHANNELS` | What makes a video "about her"; channels never shown | `yuura,yozakura`, none |
| `HEARTBEAT_SECONDS` | How often the heartbeat is edited (15+) | 60 |
| `*_FILE` | Where each file below lives | `data/...` |

---

## Data files

Everything is plain JSON in `data/`, rewritten atomically (a temporary file, then a rename), so a crash never leaves half a file. A file that cannot be read stops the bot instead of being replaced with an empty one.

| File | Holds |
|---|---|
| `state.json` | What was announced (message IDs), the last recap/reminder, X read counts |
| `history.json` | Every stream, premiere, video and Short seen, with a `kind` |
| `collabs.json` | Partners tagged by hand with `/collab` |
| `which.json` | The nickname questions and their answers |
| `outside.json` | Collab-review decisions |
| `settings.json` | What `/setup` saved |
| `roles.json` | The role panel: groups, roles, emoji, message IDs |
| `rodok.json` | Rodok counts |
| `community.json` | What was announced for the community channel |
| `partners.csv` (project root) | The partner list, edited by hand |

### `partners.csv`
Header: `name,kind,also_matches,members`

- `kind`: `partner` (one person), `group` (a team: `members` lists its partners), `duo` (a team that is named alone in the recap), `ignore` (words that must never match).
- `also_matches`: other spellings, separated by `;`. Matching is whole-word and case-insensitive.
- Several partners may share a nickname; the bot then asks which one is meant.
- A group named in a title means all its members; the recap writes the team name when all are present.
- A file with mistakes stops the bot, with every mistake listed.

---

## Costs and quotas

- **YouTube** quota: 10,000 free units a day. A check costs 4 units (the uploads list, the details, the Live tab and the Shorts list): about 2,900 a day for hers at 120 s, about 1,150 for the community channel at 300 s.
- **X** bills per post read (about $0.005). The default cap of 250 reads a month is about $1.25.
- **Gemini** is only called for captions that are mostly Japanese.

---

## Project layout

```
cmd/bot/main.go          wiring: reads the config, builds everything, starts the background jobs
internal/
  config/                .env settings, validated all at once
  settings/              channels and roles that /setup can change while running
  store/ history/        the state file and the stream history
  youtube/ poller/       reading YouTube, and the loop that checks it
  broadcast/             turns a stream into one message that is edited as it changes
  catchup/               "Back online" report after a restart
  recap/                 the weekly recap and the Saturday reminder
  partners/ collabs/     the partner list, and who was in which stream
  which/                 "who is meant?" questions for shared nicknames
  outside/               collabs on other channels: intake, review, buttons
  xapi/ relay/           reading X, and relaying posts
  tweetcard/ translate/  the Components V2 card, and Japanese detection + Gemini
  rolepanel/             the role panel (groups, roles, emoji, publishing)
  rodok/                 the rodok counter and its limits
  discord/               slash commands, buttons, the Discord adapters
  msg/                   every text the bot says
  ops/                   the heartbeat
  atomicfile/            safe file writing
```

A few rules the code follows: every background job takes a `context` and stops cleanly on shutdown; Discord and YouTube are reached through small interfaces, so most packages work without the network; the bot never pings anyone except the one role a message is meant for.

---

## Status

The bot is feature-complete and in an observation phase: things are checked by watching the real server. New videos, Shorts and premieres (Yuura's and the community's) are announced from the code but can only be seen when one is actually published.
