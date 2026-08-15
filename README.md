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
| YouTube | A restricted API key for local public-chat mode, or a Desktop-app OAuth client for unattended server mode | [Google Cloud projects](https://console.cloud.google.com/projectcreate), [API library](https://console.cloud.google.com/apis/library/youtube.googleapis.com), and [Credentials](https://console.cloud.google.com/apis/credentials) | `streamchat setup youtube` or `streamchat setup youtube-server` |
| Kick | A Kick app Client ID and Client Secret, a browser-created user access/refresh token with `user:read events:subscribe chat:write channel:write`, and a public HTTPS webhook configured in the developer portal and mirrored in `kick.webhook_url` | [Kick Developer settings](https://kick.com/settings/developer), [Kick app setup](https://docs.kick.com/getting-started/kick-apps-setup), and [Kick OAuth 2.1](https://docs.kick.com/getting-started/generating-tokens-oauth2-flow) | `streamchat setup kick` |
| Twitch | A Twitch app Client ID and Client Secret and a browser-created user access/refresh token with only `user:read:chat`; a channel name or URL | [Twitch Developer Console](https://dev.twitch.tv/console/apps), [Twitch OAuth](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/), and [EventSub chat authentication](https://dev.twitch.tv/docs/chat/authenticating/) | `streamchat setup twitch` |

No real-looking credentials are included in this repository.

## Everyday use

```sh
streamchat run
streamchat run --youtube-video 'https://www.youtube.com/watch?v=VIDEO_ID'
streamchat run --twitch-channel 'https://www.twitch.tv/CHANNEL'
streamchat config show
streamchat config check
streamchat archive stats
```

`run` activates configured platforms through the internal platform registry. If a configured YouTube or Twitch target is missing, Streamchat asks for the live URL/video ID or channel name rather than asking for a numeric platform ID.

While `streamchat run` is displaying incoming chat, select Kick as the outbound target and then type plain messages:

```text
/kick
hello
```

The selection-and-send shorthand is:

```text
/kick hello
```

`/kick` selects Kick as the current outbound target. Subsequent plain lines go to Kick until another target-selection command is used. Streamchat immediately stores the canonical target name (`kick`) in `${XDG_STATE_HOME:-$HOME/.local/state}/streamchat/client.json` and restores it on the next `streamchat run` when that sender is available. Missing, invalid, or unavailable saved targets fall back to `[NONE] >`; before a target is selected, plain text is not sent. The state file contains no credentials. Successful sends rely on the normal incoming Kick event for display rather than printing a separate local echo.

Basic channel controls currently target Kick only and are independent of `/kick` selection:

```text
/title New stream title
/category Just Chatting
```

`/title` updates the authenticated Kick channel after Kick confirms the request. `/category` accepts a positive numeric category ID or searches official Kick categories by name. A single case-insensitive exact match is preferred, a sole search result is accepted, and multiple plausible results are listed with IDs without changing the category.

Basic moderation controls also currently target Kick only and are independent of `/kick` selection:

```text
/ban kick USER
/timeout kick USER 10m
```

The platform is always explicit and is not inferred from `/kick` or any future outbound target; only `kick` is currently supported. `/ban kick` permanently bans the resolved Kick user. `/timeout kick` accepts `s`, `m`, or `h` syntax but must resolve to the official API's whole-minute range of 1–10,080 minutes; for example, `10m`, `1h`, and `60s` are valid, while `1s` and `30s` are not. Kick may remove affected messages from visible chat. These commands never delete Streamchat SQLite archive rows; archived chat remains historical and append-preserving.

Local display cleaning is available in the interactive terminal:

```text
/clean streamchat
/clean kick
/clean USER
```

`/clean streamchat` removes all messages from the current local view, `/clean kick` removes displayed Kick messages, and `/clean USER` removes displayed messages from a case-insensitive author match. The words `streamchat`, `kick`, `youtube`, and `twitch` are reserved clean targets rather than usernames; YouTube and Twitch display cleaning are not implemented yet. `/clean` affects only the current Streamchat display. It never changes platform chat and never deletes archived messages. The interactive view keeps only the latest 500 rendered messages for local redraws; it does not retrieve history from SQLite.

Remote visible-chat cleanup is a separate, explicit command:

```text
/clear kick
/clear kick 3d
```

`/clear kick` attempts to delete archived Kick messages from the default recent window of 24 hours. `/clear kick 3d` uses archived Kick messages from the last three days; any positive `Nd` value up to 3650 days is accepted. Streamchat queries only distinct, non-empty Kick provider message IDs and timestamps from the configured SQLite archive, completes that snapshot, then deletes each ID individually with Kick's official API. Messages archived after the snapshot begins are not included in that run, and an already-absent message counts as cleared.

Kick exposes only per-message deletion, so archived provider IDs let Streamchat approximate bulk clearing. Messages posted before Streamchat observed and archived them cannot be cleared this way. The configured `storage.sqlite_path` must be accessible to the interactive client; this may require running the command where the server archive resides or making that private database path available locally. `/clear kick` does not automatically remove messages from the local display. `/clear youtube` and `/clear twitch` are not implemented. Neither `/clean` nor `/clear` ever deletes Streamchat SQLite archive records.

Open a configured stream in an external player or browser with an explicit platform:

```text
/open kick
/open youtube
/open twitch
```

On Linux, Streamchat prefers `mpv` and passes it the normal public platform URL so mpv/yt-dlp can resolve playback. If `mpv` is unavailable, Streamchat uses `xdg-open` to launch the same URL through the desktop's default handler. The child is detached from Streamchat's terminal input and output so the interactive UI remains responsive. Kick resolves the authenticated channel's public slug; YouTube and Twitch require an available configured video ID or channel.

Exit the interactive client cleanly with either command:

```text
/exit
/quit
```

Both commands work regardless of the selected outbound target, are never sent to chat, and apply only to `streamchat run`. They cancel active adapters so relay/WebSocket connections use their normal shutdown path before the client exits successfully.

When standard input and output are terminals, `streamchat run` uses the terminal's alternate screen with three fixed Kick status lines at the top, scrolling chat in the middle, and a persistent single-line input bar at the bottom:

```text
Title:    Current stream title                         2026-08-15
Category: Just Chatting                                      16:07
Viewers:  42
-------------------------------------------------------------
chat
-------------------------------------------------------------
[KICK] >
```

Status is fetched immediately from Kick's official authenticated channel endpoint, refreshed every 30 seconds, and refreshed after successful `/title` and `/category` commands. The interactive client shows its local date and time at the right edge and updates them once per minute. Offline streams show `Viewers:  OFFLINE`; until the first successful fetch, all three values show `unavailable`. Transient refresh failures preserve the previous status.

The input starts as `[KICK] >` when a saved, currently available Kick target is restored; otherwise it starts as `[NONE] >` and changes after `/kick`. Incoming messages are confined between the fixed separators without discarding the current input or cursor position. Provider-neutral author roles appear as temporary letter badges in `[B][M][P][V][O][S][F]` order; raw provider badges remain in the normalized message data. `/clean` redraws only the chat region while retaining status, separators, and input. Basic Unicode insertion, Backspace, Left/Right, Home/End, Enter, Ctrl-C, and Ctrl-D are supported; long input scrolls horizontally to keep the cursor visible.

Slash commands have prefix-based, hierarchical completion in the interactive terminal. Type a prefix such as `/cl` to see matching commands, use Tab or Up/Down to select, Enter to accept an active suggestion, and Esc to dismiss the popup. Structured second tokens are suggested for commands such as `/clear`, `/ban`, `/timeout`, `/open`, and `/clean`; free-form titles, categories, usernames, durations, and chat text are not suggested. Completion never executes a command automatically.

Every exit path restores raw mode and the original shell screen. Piped/non-TTY input retains the line-oriented behavior without status fetching, alternate-screen, completion UI, or redraw sequences.

Kick emotes use their structured webhook metadata. In an interactive Linux terminal, `emotes.mode: "auto"` optionally starts one [Überzug++](https://github.com/jstkdng/ueberzugpp) helper and places cached images in terminal cells; Wayland/Sway uses Überzug++'s Wayland output. If the `ueberzug` executable or a suitable local graphical terminal is unavailable, an image is still downloading, or a download fails, Streamchat displays readable text such as `:ppJedi:`. SSH and non-TTY sessions always use that dependency-free fallback. `text` always uses text, while `off` disables graphical handling but retains readable emote text.

```json
{
  "emotes": {
    "mode": "auto",
    "debug": false
  }
}
```

Images are fetched asynchronously from Kick's provider-derived official asset URL, limited to 2 MiB, deduplicated while downloading, and persisted under `${XDG_CACHE_HOME:-$HOME/.cache}/streamchat/emotes/kick/` using numeric emote IDs rather than names. Animated GIF assets use a cached first-frame PNG preview in the terminal so repeated viewport repositioning stays lightweight and stable. Streamchat disables Überzug++'s separate resized-image cache so stale low-resolution derivatives cannot override the current terminal-cell size. Image overlays are recomputed from the bounded visible-message model during chat scroll, resize, and `/clean`, and are removed before alternate-screen exit. A readable `:emoteName:` backing remains until the image placement has been submitted successfully. Emote presentation never rewrites or deletes archived message data.

For troubleshooting, set `emotes.debug` to `true` or `STREAMCHAT_EMOTES_DEBUG=true`. Sanitized backend/cache events are written privately to `${XDG_CACHE_HOME:-$HOME/.cache}/streamchat/emotes/debug.log`; normal chat output is unchanged. Debug mode captures Überzug++ startup, PID, sanitized placement commands, socket/process failures, and helper stderr. Normal mode keeps the helper silent. The log never includes OAuth or relay credentials.

## Server/client mode and archive

Run `streamchat serve` on the utility VM and let the interactive machine connect to it. The server receives verified Kick webhooks, discovers and streams the authenticated YouTube account's active live chat, writes every accepted normalized Kick/YouTube event to SQLite, then relays it live. Twitch remains on the interactive client for now. There is no history replay. Kick chatback is sent directly from the interactive client to Kick's official API using its locally stored OAuth token; it does not pass through `/relay` and adds no public endpoint.

```text
Kick → public VPS HTTPS reverse proxy ─┐
                                      ├→ utility VM streamchat serve → SQLite
YouTube official API streamList ──────┘                 ↓ private WebSocket
                                                 main machine streamchat run
                                                         ↓ official Kick API
                                                        Kick
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
  "storage": {
    "sqlite_path": "/var/lib/streamchat/streamchat.db"
  },
  "relay_auth_token": "PASTE_THE_SHARED_RANDOM_TOKEN_HERE"
}
```

Authorize YouTube once as the service account. Create a Google OAuth Client ID with application type **Desktop app**, then run:

```sh
streamchat setup youtube-server --config /etc/streamchat/config.json
```

The browser flow uses the loopback callback `http://127.0.0.1:8791`, PKCE, and only `youtube.readonly`. It requests offline access and stores the refresh token in the mode-`0600` config, allowing the service to refresh access without the Gentoo client. On a headless VM, use SSH port forwarding (`ssh -L 8791:127.0.0.1:8791 utility-vm`) and open the printed Google URL on your workstation. By default the server calls authenticated `liveBroadcasts.list` to find the active broadcast and its `snippet.liveChatId`; setting `youtube.video_id` retains an explicit broadcast/video override.

Google's official `liveChatMessages.streamList` HTTP transport supplies live messages and a continuation token. Streamchat reconnects with that token after recoverable failures and waits between broadcasts so the service can remain running continuously.

Start it with:

```sh
streamchat serve --config /etc/streamchat/config.json
```

The default listen address is `127.0.0.1:8788`. An explicit loopback or private IP is accepted; wildcard and public-IP binds are rejected. Streamchat serves plain HTTP/WebSocket only. The VPS must terminate public TLS and forward only `/webhooks/kick` to the utility VM. Do not expose `/relay` through that public proxy. A private encrypted network such as ZeroTier can carry `ws://` relay traffic directly.

In the Kick developer portal, configure the application webhook as `https://streamchat.sleepymario.com/webhooks/kick` (or the equivalent public URL for another deployment), and keep `kick.webhook_url` set to that same value. The local JSON field is only a validated operator reference: Streamchat does not transmit it when creating an event subscription, so changing the JSON alone does not change Kick's destination. After changing the webhook URL in the developer portal, run:

```sh
streamchat kick subscribe
```

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

The client sends the token only in the WebSocket `Authorization: Bearer` header, never in the URL. When a remote server URL is configured, `run` consumes relayed Kick and YouTube messages instead of starting those local adapters; configured Twitch still runs normally. The legacy local-only YouTube and `streamchat kick serve` commands remain available.

Kick chatback and channel controls require the interactive machine's config to contain the Kick OAuth credentials and broadcaster ID created by `streamchat setup kick`. Those credentials are used only for direct official Kick API requests and are never sent to the Streamchat server or relay.

The archive defaults to `/var/lib/streamchat/streamchat.db`. The example systemd unit uses `StateDirectory=streamchat`, so systemd creates that private writable directory for the dedicated `streamchat` account despite the read-only filesystem sandbox. Verify ingestion without installing the `sqlite3` CLI:

```sh
streamchat archive stats --config /etc/streamchat/config.json
```

The versioned schema stores platform, broadcast/channel ID, provider message ID, event time, user ID/display name, text, event type, represented moderation/deletion state, emotes, selected safe provider metadata, and the complete normalized message. `(platform, message_id)` is unique, so reconnect deliveries are not duplicated. An archive write failure stops the server before relay rather than silently losing data.

Chat archives contain viewer names, IDs, messages, payment/membership metadata, and moderation events. Treat the database and backups as personal data, limit access and retention appropriately, and remove the database and its backups when the archive is no longer required.

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

For local public live chat, the API key remains sufficient. Streamchat uses official `videos.list` to discover `activeLiveChatId`, then polls official `liveChatMessages.list`, preserving `nextPageToken` and honoring `pollingIntervalMillis`.

For the utility server, `streamchat setup youtube-server` performs Google's official desktop OAuth authorization-code flow with PKCE and offline access. It requests only `youtube.readonly`, stores and refreshes the access/refresh tokens, discovers the authenticated account's active broadcast through `liveBroadcasts.list`, and consumes the preferred lower-latency `liveChatMessages.streamList` endpoint. It does not request chat-write or moderation access.

### Kick

Kick requires an application created in Developer settings. Its Client ID identifies the app, while its Client Secret authenticates OAuth exchanges and must remain private. Streamchat runs an OAuth 2.1 authorization-code flow with PKCE on a loopback callback and requests:

- `user:read` to retrieve the authorized user's numeric broadcaster ID automatically;
- `events:subscribe` to create the `chat.message.sent` version 1 subscription;
- `chat:write` to send chat messages from the interactive CLI;
- `channel:write` to update the authenticated channel's stream title and category;
- `channel:read` to fetch channel status and resolve moderation targets through official channel slugs;
- `moderation:ban` to ban and timeout users;
- `moderation:chat_message:manage` to delete known messages from visible Kick chat with `/clear kick`.

The user does not need to discover a broadcaster ID or paste a temporary token. Streamchat stores and rotates the access/refresh tokens. Existing installations must enable the listed scopes in the Kick developer portal and rerun `streamchat setup kick`; `/clear kick` specifically requires `moderation:chat_message:manage`. Category lookup uses Kick's category endpoints with the same user token. Streamchat does not request stream-key or rewards scopes.

Kick sends events only to the publicly reachable HTTPS webhook configured in the Kick developer application. `kick.webhook_url` must match that portal setting, but Streamchat does not send the local value in the `events/subscriptions` request; `streamchat kick subscribe` creates `method=webhook` subscriptions using the destination already held by Kick. Changing only the local JSON does not update that destination. After changing the portal URL, run `streamchat kick subscribe`.

In server/client mode, a trusted TLS reverse proxy forwards only `/webhooks/kick` to the private address running `streamchat serve`. In legacy local-only mode, a tunnel or reverse proxy can still map the public webhook to `http://127.0.0.1:8788/webhooks/kick`.

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

- YouTube: `STREAMCHAT_YOUTUBE_API_KEY`, `STREAMCHAT_YOUTUBE_CLIENT_ID`, `STREAMCHAT_YOUTUBE_CLIENT_SECRET`, `STREAMCHAT_YOUTUBE_ACCESS_TOKEN`, `STREAMCHAT_YOUTUBE_REFRESH_TOKEN`, `STREAMCHAT_YOUTUBE_REDIRECT_URI`, `STREAMCHAT_YOUTUBE_VIDEO_ID`
- Kick: `STREAMCHAT_KICK_CLIENT_ID`, `STREAMCHAT_KICK_CLIENT_SECRET`, `STREAMCHAT_KICK_ACCESS_TOKEN`, `STREAMCHAT_KICK_REFRESH_TOKEN`, `STREAMCHAT_KICK_BROADCASTER_ID`, `STREAMCHAT_KICK_WEBHOOK_URL`, `STREAMCHAT_KICK_REDIRECT_URI`, `STREAMCHAT_KICK_LISTEN`
- Twitch: `STREAMCHAT_TWITCH_CLIENT_ID`, `STREAMCHAT_TWITCH_CLIENT_SECRET`, `STREAMCHAT_TWITCH_ACCESS_TOKEN`, `STREAMCHAT_TWITCH_REFRESH_TOKEN`, `STREAMCHAT_TWITCH_USER_ID`, `STREAMCHAT_TWITCH_USER_LOGIN`, `STREAMCHAT_TWITCH_REDIRECT_URI`, `STREAMCHAT_TWITCH_CHANNEL`
- Server: `STREAMCHAT_SERVER_LISTEN`, `STREAMCHAT_SERVER_WEBSOCKET_PATH`
- Client: `STREAMCHAT_CLIENT_SERVER_URL`
- Relay authentication: `STREAMCHAT_RELAY_AUTH_TOKEN`
- Storage: `STREAMCHAT_STORAGE_SQLITE_PATH`
- Output: `STREAMCHAT_LOG_FILE`
- Emotes: `STREAMCHAT_EMOTES_MODE` (`auto`, `text`, or `off`), `STREAMCHAT_EMOTES_DEBUG` (`true` or `false`)

See `examples/config.example.json` for the manual JSON shape. Environment-provided access tokens are used in memory and are not written back during refresh.

## Architecture

- `internal/chat`: normalized credential-free message and adapter contract
- `internal/platform`: registry/factory and platform selection
- `internal/platform/youtube`: official Data/Live Streaming API adapter
- `internal/platform/kick`: OAuth, chat sending, channel controls, subscriptions, verified webhook receiver
- `internal/outbound`: canonical outbound target selection and command aliases
- `internal/clientstate`: atomic, non-secret interactive client preferences
- `internal/platform/twitch`: OAuth/API support and EventSub WebSocket adapter
- `internal/relay`: authenticated in-memory WebSocket broadcast server/client
- `internal/archive`: versioned SQLite storage and operational statistics
- `internal/setup`: interactive, safely rerunnable setup wizard
- `internal/config`: JSON/env/defaults, atomic writer, validation, redaction
- `internal/aggregate`: bounded chronological merge and duplicate cache
- `internal/render`: terminal frontend and injection defense
- `internal/emote`: provider-neutral fallback formatting, persistent asset cache, and optional image backend
- `internal/terminalui`: raw-mode key editing and synchronized persistent input bar
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

The example unit runs as the dedicated `streamchat` user, reads `/etc/streamchat/config.json`, and uses systemd's `StateDirectory=streamchat` to create `/var/lib/streamchat`; it does not embed secrets. Create the service account and private configuration directory before installing the example as a real unit:

```sh
sudo useradd --system --home-dir /var/lib/streamchat --shell /usr/sbin/nologin streamchat
sudo install -d -m 0750 -o streamchat -g streamchat /etc/streamchat
sudo install -m 0600 -o streamchat -g streamchat /usr/share/doc/streamchat/examples/config.example.json /etc/streamchat/config.json
sudo install -m 0644 /usr/share/doc/streamchat/examples/systemd/streamchat.service /etc/systemd/system/streamchat.service
```

The `.deb` includes the updated unit under `/usr/share/doc/streamchat/examples/systemd/` and keeps the existing single-package model.

## License

MIT. See `LICENSE`.
