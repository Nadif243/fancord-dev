# Two stages: the first compiles the bot, the second is the small image that actually runs. Nothing secret goes in here: the token comes from .env when the container starts (see docker-compose.yml).

# ---- stage 1: build ---------------------------------------------------------
# Set this to your own Go version (run `go version`) if the build says go.mod needs a newer one.
ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-alpine AS build

# The official Go images turn automatic toolchain download off. Turning it back on lets go.mod ask for a slightly newer Go than the image has.
ENV GOTOOLCHAIN=auto

WORKDIR /src

# Download the libraries first, on their own. Docker remembers this step, so changing the bot's code does not download everything again.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off: a plain, self-contained program that needs nothing from the system.
# -s -w drops debug information (smaller file). VERSION shows in the heartbeat.
ARG VERSION=0.1.0-docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/bot ./cmd/bot

# ---- stage 2: run -----------------------------------------------------------
FROM alpine:3.22

# ca-certificates: needed to talk to Discord and YouTube over HTTPS.
# tzdata: lets the container log in Jakarta time (TZ is set in the compose file).
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 bot \
 && mkdir -p /app/data \
 && chown bot /app/data

WORKDIR /app
COPY --from=build /out/bot /app/bot

# Do not run as root. The bot has no need for it.
USER bot

# The relative paths in the bot's settings (data/history.json and so on) are relative to this folder, so they end up in /app/data.
ENTRYPOINT ["/app/bot"]
