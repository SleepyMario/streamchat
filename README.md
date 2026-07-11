# Streamchat

Streamchat is a CLI-first Go application that receives live chat through the official YouTube and Kick APIs, normalizes events, merges them in bounded chronological order, and prints a safe terminal stream. There is no GUI yet; adapters, aggregation, rendering, configuration, and logging are separate packages so a future frontend can reuse the core.

## Build and try it

```sh
make build
make test
./bin/streamchat demo --no-color --timestamps
```

The offline demo uses the real normalized model, aggregator, duplicate cache, and renderer. It includes YouTube and Kick messages, badges, a reply, emote metadata, a paid event, and a duplicate delivery.

## Architecture

- `cmd/streamchat`: commands, process lifecycle, signal handling
- `internal/chat`: normalized message and adapter contract
- `internal/aggregate`: bounded merge queue and bounded duplicate cache
- `internal/platform/youtube`: official Data/Live Streaming API adapter
- `internal/platform/kick`: official webhook verifier, receiver, and subscription client
- `internal/render`: streaming terminal frontend and terminal-injection defense
- `internal/logging`: opt-in JSONL writer
- `internal/config`: JSON, environment, defaults, validation, and redaction

## Commands

```text
streamchat --help
streamchat version
streamchat demo [--no-color] [--timestamps]
streamchat youtube --youtube-video ID --youtube-api-key KEY
streamchat kick serve [--kick-listen 127.0.0.1:8788]
streamchat kick subscribe --kick-broadcaster-id ID
streamchat kick subscribe --inspect --kick-broadcaster-id ID
streamchat run [YouTube and/or Kick flags]
streamchat config check [--config FILE]
```

`run` requires at least one configured platform. `kick serve` receives `POST /webhooks/kick`; `GET /healthz` returns only a basic status.

## YouTube setup

Create a Google Cloud project, enable YouTube Data API v3, and create an API key restricted appropriately. Streamchat calls official `videos.list` with `snippet,liveStreamingDetails` to discover `activeLiveChatId`, then polls official `liveChatMessages.list`. It preserves `nextPageToken`, waits for every returned `pollingIntervalMillis`, uses bounded jittered retry for transient failures, and stops for `offlineAt`, `chatEndedEvent`, or `liveChatEnded`.

An API key is sufficient for public `videos.list` and public live-chat reads when the API permits that resource. OAuth is required for private or ownership-sensitive resources and operations, and may be supplied with `STREAMCHAT_YOUTUBE_ACCESS_TOKEN`. Streamchat does not implement an OAuth browser flow or write/moderation operations. Google documents `streamList` as the preferred low-latency method, but its supported server-streaming client transport is not implemented in this standard-library initial version; polling is fully supported and honors the official interval.

## Kick setup and webhook security

Register an app in the Kick developer portal and configure its webhook URL. Kick needs a publicly reachable HTTPS callback; Streamchat deliberately listens on `127.0.0.1:8788`, so terminate TLS and proxy only `/webhooks/kick` through a reverse proxy.

Kick sends `chat.message.sent` version 1. Streamchat requires the documented message ID, subscription ID, signature, timestamp, type, and version headers. It verifies RSA PKCS#1 v1.5 with SHA-256 over:

```text
Kick-Event-Message-Id + "." + Kick-Event-Message-Timestamp + "." + raw request body
```

The official Kick public key is bundled from the documentation. Verification fails closed. Events older or farther in the future than five minutes, malformed requests, unsupported events, invalid signatures, and bodies over 1 MiB are rejected. Event IDs are deduplicated in bounded, expiring memory.

To create a subscription, obtain a Kick user or app access token with `events:subscribe`, set the broadcaster user ID when using an app token, and run:

```sh
STREAMCHAT_KICK_ACCESS_TOKEN=... streamchat kick subscribe --kick-broadcaster-id 123
```

Use `--inspect` to call the official GET endpoint. Streamchat never claims success when the token, scope, or broadcaster ID is missing. OAuth token acquisition and refresh are intentionally outside this initial release.

## Configuration

Default file: `${XDG_CONFIG_HOME:-$HOME/.config}/streamchat/config.json`. Precedence is command-line flags, environment variables, JSON file, then safe defaults.

Environment variables:

- `STREAMCHAT_YOUTUBE_API_KEY`
- `STREAMCHAT_YOUTUBE_ACCESS_TOKEN`
- `STREAMCHAT_YOUTUBE_VIDEO_ID`
- `STREAMCHAT_KICK_CLIENT_ID`
- `STREAMCHAT_KICK_CLIENT_SECRET`
- `STREAMCHAT_KICK_ACCESS_TOKEN`
- `STREAMCHAT_KICK_BROADCASTER_ID`
- `STREAMCHAT_KICK_WEBHOOK_URL`
- `STREAMCHAT_KICK_LISTEN`
- `STREAMCHAT_LOG_FILE`

See `examples/config.example.json`. Secret values are not printed and tokens are not persisted automatically. `config check` accepts an empty/demo-only configuration while still checking safe structural defaults.

## Terminal and logging safety

The renderer preserves Unicode but removes ANSI/OSC escape sequences and other control characters from all untrusted fields. It does not execute links, parse markup, or render terminal images. Emote text remains in the message and normalized emote metadata remains available to other frontends. Color is disabled for `--no-color`, `NO_COLOR`, or non-terminal stdout.

JSONL logging is disabled by default. `--log-file PATH` appends one normalized message per line and creates the file with mode `0600`. A write or flush failure terminates the process rather than silently losing log data. Logs can contain usernames, platform IDs, message text, replies, badges, and other personal data; protect and delete them according to applicable privacy requirements.

## Reliability and limits

Queues, retry delays, request bodies, and duplicate tracking are bounded. SIGINT/SIGTERM cancel adapters and gracefully stop the webhook server. HTTP clients have timeouts. Permanent authentication errors are not retried forever.

This initial version does not implement YouTube gRPC `streamList`, Kick OAuth token acquisition/refresh, rich terminal emote images, moderation actions, persistent cross-restart deduplication, or a GUI. Chronological ordering uses a small local reorder window; it is not distributed event-time synchronization.

## Development

```sh
make fmt
make vet
make test
make test-race
make check
```

Tests use `httptest`, injected clients, invented users, local RSA fixtures, and fake clocks. They perform no external API calls.

Troubleshooting: a YouTube 401/403 usually means invalid credentials or resource permissions; `liveChatEnded` is a normal stop. Kick 401 webhook responses indicate signature failure, while 400 indicates missing/stale headers or an unsupported event. Repeated webhook failures can cause Kick to disable the subscription, so inspect logs and resubscribe after fixing the endpoint.

## License

MIT. See `LICENSE`.
