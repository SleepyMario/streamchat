# Streamchat

Streamchat reads live chat from YouTube, Kick, and Twitch, merges messages chronologically, and prints a safe terminal stream. It uses only documented official platform APIs.

## Start here

```sh
make build
./bin/streamchat setup
./bin/streamchat run
```

On a first run with no usable configuration, `streamchat` offers the setup wizard automatically. The wizard lets you select any combination of the three supported platforms, explains every credential before asking for it, opens browser authorization when appropriate, and stores the result privately.

| Platform | What Streamchat needs | Where to get it | Setup command |
| --- | --- | --- | --- |
| YouTube | A Google Cloud API key restricted to YouTube Data API v3; a live URL or video ID when running | [Google Cloud projects](https://console.cloud.google.com/projectcreate), [API library](https://console.cloud.google.com/apis/library/youtube.googleapis.com), and [Credentials](https://console.cloud.google.com/apis/credentials) | `streamchat setup youtube` |
| Kick | A Kick app Client ID and Client Secret, a browser-created user access/refresh token with `user:read events:subscribe`, and a public HTTPS webhook ending in `/webhooks/kick` | [Kick Developer settings](https://kick.com/settings/developer), [Kick app setup](https://docs.kick.com/getting-started/kick-apps-setup), and [Kick OAuth 2.1](https://docs.kick.com/getting-started/generating-tokens-oauth2-flow) | `streamchat setup kick` |
| Twitch | A Twitch app Client ID and Client Secret and a browser-created user access/refresh token with only `user:read:chat`; a channel name or URL | [Twitch Developer Console](https://dev.twitch.tv/console/apps), [Twitch OAuth](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/), and [EventSub chat authentication](https://dev.twitch.tv/docs/chat/authenticating/) | `streamchat setup twitch` |

No real-looking credentials are included in this repository.

## Everyday use

```sh
streamchat run
streamchat run --youtube-video 'https://www.youtube.com/watch?v=VIDEO_ID'
streamchat run --twitch-channel 'https://www.twitch.tv/CHANNEL'
streamchat config show
streamchat config check
```

`run` activates configured platforms through the internal platform registry. If a configured YouTube or Twitch target is missing, Streamchat asks for the live URL/video ID or channel name rather than asking for a numeric platform ID.

## Minimal server/client mode

For an always-on Kick webhook, run `streamchat serve` on a utility VM and let the interactive machine connect to it. This first server mode relays only new Kick messages; it has no history, database, or outgoing-chat support.

```text
Kick → public VPS HTTPS reverse proxy → utility VM streamchat serve
                                          ↓ private WebSocket (for example ZeroTier)
                                      main machine streamchat run
```

Generate one shared token and store the same value in private mode-`0600` configuration files on both machines:

```sh
openssl rand -hex 32
```

Utility VM configuration (`/etc/streamchat/config.json` in the systemd example):

```json
{
  "server": {
    "listen": "10.147.0.10:8788",
    "websocket_path": "/relay"
  },
  "relay_auth_token": "PASTE_THE_SHARED_RANDOM_TOKEN_HERE"
}
```

Start it with:

```sh
streamchat serve --config /etc/streamchat/config.json
```

The default listen address is `127.0.0.1:8788`. An explicit loopback or private IP is accepted; wildcard and public-IP binds are rejected. Streamchat serves plain HTTP/WebSocket only. The VPS must terminate public TLS and forward only `/webhooks/kick` to the utility VM. Do not expose `/relay` through that public proxy. A private encrypted network such as ZeroTier can carry `ws://` relay traffic directly.

Main-machine configuration can be merged into the existing user config:

```json
{
  "client": {
    "server_url": "ws://10.147.0.10:8788/relay"
  },
  "relay_auth_token": "PASTE_THE_SHARED_RANDOM_TOKEN_HERE"
}
```

Then run the usual client command:

```sh
streamchat run
```

The client sends the token only in the WebSocket `Authorization: Bearer` header, never in the URL. When a remote server URL is configured, `run` consumes relayed Kick messages instead of opening the local Kick webhook listener; configured YouTube and Twitch adapters still run normally. The legacy local-only `streamchat kick serve` command remains available.

The older expert commands remain available:

```sh
streamchat youtube --youtube-video VIDEO_ID
streamchat kick serve
streamchat kick subscribe
streamchat kick subscribe --inspect
streamchat demo --no-color --timestamps
```

## What setup does

### YouTube

The wizard explains how to create/select a Google Cloud project, enable YouTube Data API v3, create an API key, and restrict that key to the YouTube Data API. It validates a newly entered key with one low-cost `videos.list` request.

For public live chat, the API key is sufficient. Streamchat uses official `videos.list` to discover `activeLiveChatId`, then polls official `liveChatMessages.list`, preserving `nextPageToken` and honoring `pollingIntervalMillis`. An optional OAuth access token remains supported for private or ownership-sensitive resources, but setup does not request OAuth unnecessarily. Google documents `streamList` as the preferred lower-latency future path; Streamchat currently retains its tested polling implementation.

### Kick

Kick requires an application created in Developer settings. Its Client ID identifies the app, while its Client Secret authenticates OAuth exchanges and must remain private. Streamchat runs an OAuth 2.1 authorization-code flow with PKCE on a loopback callback and requests:

- `user:read` to retrieve the authorized user's numeric broadcaster ID automatically;
- `events:subscribe` to create the `chat.message.sent` version 1 subscription.

The user does not need to discover a broadcaster ID or paste a temporary token. Streamchat stores and rotates the access/refresh tokens.

Kick sends events only to a publicly reachable HTTPS webhook configured for the app. In server/client mode, a trusted TLS reverse proxy forwards only `/webhooks/kick` to the private address running `streamchat serve`. In legacy local-only mode, a tunnel or reverse proxy can still map the public webhook to `http://127.0.0.1:8788/webhooks/kick`.

Webhook verification remains fail-closed. Streamchat verifies Kick's RSA/SHA-256 signature over the documented message ID, timestamp, and raw body, enforces freshness/body limits, and deduplicates deliveries.

### Twitch

The wizard asks the user to register the displayed localhost redirect URI in the [Twitch Developer Console](https://dev.twitch.tv/console/apps). It then opens the Twitch authorization page and always prints the URL as a fallback. The callback binds only to loopback, validates a cryptographically random OAuth state value, and exchanges the returned code for access and refresh tokens.

Streamchat requests only `user:read:chat`, the minimum permission for this read-only installed chat client. It does not request `user:write:chat`, moderation, bot, or broadcaster-management permissions. It validates the token, stores rotated refresh tokens, resolves channel names to numeric IDs through `GET /helix/users`, and subscribes to `channel.chat.message` version 1 over the official EventSub WebSocket transport.

The adapter handles welcome, notification, keepalive, reconnect, revocation, duplicate delivery, and shutdown. Before connecting, Streamchat validates the token and refreshes and atomically persists it when required. Twitch badges and emotes are normalized into the shared `chat.Message` model.

## Configuration and security

Default path:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/streamchat/config.json
```

Precedence, highest first:

1. command-line flags;
2. `STREAMCHAT_*` environment variables;
3. JSON configuration;
4. safe defaults.

The setup writer creates missing directories, writes mode `0600`, and atomically replaces the file. Re-running setup asks before replacing existing platform values and preserves other platforms. `streamchat config show` redacts API keys, client secrets, access tokens, and refresh tokens. Secret prompts disable terminal echo. Tokens and client secrets are never included in logged URLs or errors.

Persistent credentials live in the config. YouTube live URLs/video IDs and Twitch channels can be supplied per run; a default Twitch channel may also be saved for convenience.

Advanced environment variables:

- YouTube: `STREAMCHAT_YOUTUBE_API_KEY`, `STREAMCHAT_YOUTUBE_ACCESS_TOKEN`, `STREAMCHAT_YOUTUBE_VIDEO_ID`
- Kick: `STREAMCHAT_KICK_CLIENT_ID`, `STREAMCHAT_KICK_CLIENT_SECRET`, `STREAMCHAT_KICK_ACCESS_TOKEN`, `STREAMCHAT_KICK_REFRESH_TOKEN`, `STREAMCHAT_KICK_BROADCASTER_ID`, `STREAMCHAT_KICK_WEBHOOK_URL`, `STREAMCHAT_KICK_REDIRECT_URI`, `STREAMCHAT_KICK_LISTEN`
- Twitch: `STREAMCHAT_TWITCH_CLIENT_ID`, `STREAMCHAT_TWITCH_CLIENT_SECRET`, `STREAMCHAT_TWITCH_ACCESS_TOKEN`, `STREAMCHAT_TWITCH_REFRESH_TOKEN`, `STREAMCHAT_TWITCH_USER_ID`, `STREAMCHAT_TWITCH_USER_LOGIN`, `STREAMCHAT_TWITCH_REDIRECT_URI`, `STREAMCHAT_TWITCH_CHANNEL`
- Server: `STREAMCHAT_SERVER_LISTEN`, `STREAMCHAT_SERVER_WEBSOCKET_PATH`
- Client: `STREAMCHAT_CLIENT_SERVER_URL`
- Relay authentication: `STREAMCHAT_RELAY_AUTH_TOKEN`
- Output: `STREAMCHAT_LOG_FILE`

See `examples/config.example.json` for the manual JSON shape. Environment-provided access tokens are used in memory and are not written back during refresh.

## Architecture

- `internal/chat`: normalized credential-free message and adapter contract
- `internal/platform`: registry/factory and platform selection
- `internal/platform/youtube`: official Data/Live Streaming API adapter
- `internal/platform/kick`: OAuth, subscriptions, verified webhook receiver
- `internal/platform/twitch`: OAuth/API support and EventSub WebSocket adapter
- `internal/relay`: authenticated in-memory WebSocket broadcast server/client
- `internal/setup`: interactive, safely rerunnable setup wizard
- `internal/config`: JSON/env/defaults, atomic writer, validation, redaction
- `internal/aggregate`: bounded chronological merge and duplicate cache
- `internal/render`: terminal frontend and injection defense
- `internal/logging`: opt-in private JSONL writer
- `cmd/streamchat`: human CLI, lifecycle, and signal handling

JSONL logging is disabled by default and files are mode `0600`. Chat logs can contain personal data. Terminal output strips control sequences from untrusted platform content.

## Development

```sh
make check
```

Tests use fake adapters, `httptest` servers, invented fixtures, and fake WebSockets. They do not call external services.

## Debian and Ubuntu package

Build an amd64 package with the locally installed Go toolchain and `dpkg-deb`:

```sh
make deb
sudo apt install ./dist/streamchat_<version>_amd64.deb
```

The version is derived from the exact Git tag, or from the commit date and hash when the commit is untagged. Set `VERSION=1.2.3 make deb` to override it for a release build. The package contains a statically linked `/usr/bin/streamchat`, the README and license, and configuration/systemd examples under `/usr/share/doc/streamchat/examples/`. It has no Go or libc runtime dependency; only `ca-certificates` is required for trusted HTTPS connections to the platform APIs.

The example unit runs as the dedicated `streamchat` user and reads `/etc/streamchat/config.json`; it does not embed secrets. Create the service account and private configuration directory before installing the example as a real unit.

## License

MIT. See `LICENSE`.
