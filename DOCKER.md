# Running the bot in Docker (on one's own PC)

The bot runs in a container. Credentials and the bot's memory stay on PC, outside the container.

| What | Where it lives | Why |
| ---- | -------------- | --- |
| Token and API key | `.env` (next to this file) | read when the container starts; never copied into the image |
| History, collab tags, "already announced" | `data/` folder | survives rebuilding or deleting the container |
| Partner list | `partners.csv` | edit it, then restart the container |

## One-time preparation

1. **Stop any bot that has started with `go run`** (Ctrl+C). Two copies of the bot at once would post every announcement twice.
2. Docker Desktop must be running (the whale icon says "Engine running").
3. Be in the project folder (the one with `Dockerfile`, `go.mod` and `go.sum`). `go.sum` appears after `go mod tidy`; the build needs it.
4. `.env` must be filled in as before.
5. If the `data` folder does not exist yet, create it: `mkdir data`. If it exists, the bot continues with what it already knows.
6. Add this line to `.gitignore` (the bot's memory should not be committed): `data/`

## Start, watch, stop

```
docker compose up -d --build     build the image and start the bot in the background
docker compose logs -f           watch what it says (Ctrl+C leaves the logs; the bot keeps running)
docker compose restart           restart it, for example after editing partners.csv
docker compose down              stop it and remove the container (data/ stays)
```

The first build takes a few minutes (it downloads Go and the libraries). Later builds are faster.

## What a good start looks like

In `docker compose logs`:

- `partners file ready` with the number of partners
- `collab tags file ready`
- `weekly recap is on` with the next date
- `bot is running` with `version=0.1.0-docker`

In `#bot-test`: the heartbeat message says **Online** with version `0.1.0-docker`. Try `/ping` and `/devremind`.

Then the memory test: `docker compose down`, then `docker compose up -d`. Run `/collab` and check the tags you set earlier are still there.

## Updating the bot

1. Make changes for any part of the bot codebase.
2. `docker compose up -d --build`

Docker replaces the container; `data/` is untouched.

## If something goes wrong

| What you see | Likely cause |
| ------------ | ------------ |
| `go.sum: no such file` during the build | run `go mod tidy` in the project folder, then build again |
| `go.mod requires go >= 1.xx` | set `GO_VERSION` in the `Dockerfile` (first line with `ARG`) to the version `go version` shows |
| `permission denied` for `data/...` in the logs | the container's user cannot write to the folder. In `Dockerfile`, delete the line `USER bot`, build again |
| `invalid configuration: DISCORD_TOKEN is empty` | `.env` is missing or not in the project folder |
| `no partners file` in the logs | `partners.csv` is not next to `docker-compose.yml`, so the recap has no collab line |
| the container keeps restarting | read `docker compose logs`; the last lines before each restart say why |
| announcements post twice | two copies are running: one in Docker and one from `go run` or a second container |
