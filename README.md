# Streamchat

Streamchat 3.3 is a multi-platform live-chat application for Kick, Twitch, and YouTube. It provides a native Qt 6 desktop interface, a terminal client, and an optional headless relay/archive server through one shared runtime. All three platforms support reading, sending, live status, title/category controls, moderation, recent-message clearing, and opening the active stream. Streamchat uses only documented official platform APIs.

## Start here

```sh
make build
make gui
./bin/streamchat version
./bin/streamchat setup
./bin/streamchat run
```

`make gui` builds `streamchat-gui`, its private frontend runtime, and the core server executable used by local mode. The native application starts the private components automatically; ordinary desktop users do not need to invoke the CLI.

## Native desktop application

The native `streamchat-gui` frontend exposes the same shared sending, target selection, channel-control, moderation, setup, health, archive, and shutdown operations used by the terminal client. Enter sends a message and Shift+Enter inserts a new line. All visible controls, chat text, and the composer scale together from 70% through 200% using the toolbar, Ctrl+Plus, Ctrl+Minus, or Ctrl+0.

Two connection modes are available from the control desk:

- **Local server** starts a private loopback-only server beside the GUI. It creates an ephemeral relay token, keeps the configuration file private, stores the archive in the user's Streamchat configuration directory, and shuts down with the application. It does not require a Windows Firewall exception.
- **Remote server** connects to an existing `ws://` or `wss://` Streamchat relay. This is intended for a continuously running server or VM.

The Windows installer contains the native GUI and both private runtime executables. It creates Start-menu and desktop shortcuts but does not expose a terminal-client shortcut. Uninstalling removes the application while deliberately preserving user settings, OAuth material, and the chat archive.

On a first run with no usable configuration, `streamchat` offers the setup wizard automatically. The wizard lets you select any combination of the three supported platforms, explains every credential before asking for it, opens browser authorization when appropriate, and stores the result privately.

| Platform | What Streamchat needs | Where to get it | Setup command |
| --- | --- | --- | --- |
| YouTube | A restricted API key for local public-chat mode, or a Desktop-app OAuth client for unattended server mode | [Google Cloud projects](https://console.cloud.google.com/projectcreate), [API library](https://console.cloud.google.com/apis/library/youtube.googleapis.com), and [Credentials](https://console.cloud.google.com/apis/credentials) | `streamchat setup youtube` or `streamchat setup youtube-server` |
| Kick | A Kick app Client ID and Client Secret, a browser-created user access/refresh token with `user:read events:subscribe chat:write channel:write`, and a public HTTPS webhook configured in the developer portal and mirrored in `kick.webhook_url` | [Kick Developer settings](https://kick.com/settings/developer), [Kick app setup](https://docs.kick.com/getting-started/kick-apps-setup), and [Kick OAuth 2.1](https://docs.kick.com/getting-started/generating-tokens-oauth2-flow) | `streamchat setup kick` |
| Twitch | A Twitch app Client ID and Client Secret, a browser-created user access/refresh token with exactly `user:read:chat user:write:chat channel:manage:broadcast moderator:manage:banned_users moderator:manage:chat_messages`, and a channel name or URL | [Twitch Developer Console](https://dev.twitch.tv/console/apps), [Twitch OAuth](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/), and [Twitch moderation](https://dev.twitch.tv/docs/chat/moderation/) | `streamchat setup twitch` |

To make bot replies come from a separate Twitch account, run `streamchat setup twitch-bot`. Sign in to Twitch as that bot account before approving the browser prompt. This second authorization requests only `user:read:chat` and `user:write:chat`, stores the resolved identity under `bot.twitch`, and leaves the broadcaster's `twitch` configuration unchanged. The same Twitch developer application can be reused.

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

While `streamchat run` is displaying incoming chat, select Kick, Twitch, or YouTube as the outbound target and then type plain messages:

```text
/kick
hello
/twitch
hello from Twitch
/youtube
hello from YouTube
```

The selection-and-send shorthand is:

```text
/kick hello
/twitch hello
/youtube hello
```

`/kick`, `/twitch`, and `/youtube` select the current outbound target. A message on the same command line is sent immediately; subsequent plain lines keep using that target until another target is selected. Streamchat stores only the canonical target name in `${XDG_STATE_HOME:-$HOME/.local/state}/streamchat/client.json`. In server/client mode, YouTube write operations cross the authenticated private relay control endpoint and the Google OAuth token remains on the server.

Basic channel controls follow the selected outbound target on all three platforms:

```text
/title New stream title
/category Just Chatting
```

`/title` and `/category` follow the selected outbound target. Kick behavior is unchanged. Twitch updates require the authorized user to own the selected channel and to have `channel:manage:broadcast`; older Twitch read/write-chat authorizations continue to receive and send chat but management commands ask you to rerun `streamchat setup twitch`.

`/category` accepts a positive numeric provider category ID or searches the selected provider's official category API. Kick keeps its existing matching policy. Twitch validates numeric IDs with `GET /helix/games`; name searches use `GET /helix/search/categories?first=100` and accept only one case-insensitive exact canonical name. Non-exact or multiple results are listed with IDs, without changing the channel, so Streamchat never guesses from Twitch's broad substring matches.

Moderation controls name their platform explicitly and are independent of outbound selection:

```text
/ban kick USER
/timeout kick USER 10m
/ban twitch USER
/timeout twitch USER 30s
/ban youtube USER
/timeout youtube USER 30s
```

The platform is always explicit. Kick keeps its official whole-minute timeout range of 1–10,080 minutes. Twitch and YouTube accept `s`, `m`, `h`, and `d` durations. YouTube display names are resolved to their channel IDs from the authoritative server archive; a channel ID may also be entered directly. These commands never delete Streamchat SQLite archive rows.

Local display cleaning is available in the interactive terminal:

```text
/clean streamchat
/clean kick
/clean twitch
/clean youtube
/clean USER
```

`/clean streamchat` removes all messages from the current local view, `/clean kick`, `/clean twitch`, and `/clean youtube` remove displayed messages for that provider, and `/clean USER` removes displayed messages from a case-insensitive author match. The platform words are reserved clean targets rather than usernames. `/clean` affects only the current Streamchat display. It never changes platform chat and never deletes archived messages. The interactive view keeps only the latest 500 rendered messages for local redraws; it does not retrieve history from SQLite.

Remote visible-chat cleanup is a separate, explicit command:

```text
/clear kick
/clear kick 3d
/clear twitch
/clear twitch 1d
/clear youtube
/clear youtube 3d
```

`/clear kick` attempts to delete archived Kick messages from the default recent window of 24 hours. `/clear kick 3d` uses archived Kick messages from the last three days; any positive `Nd` value up to 3650 days is accepted. Streamchat queries only distinct, non-empty Kick provider message IDs and timestamps from the configured SQLite archive, completes that snapshot, then deletes each ID individually with Kick's official API. Messages archived after the snapshot begins are not included in that run, and an already-absent message counts as cleared.

Kick and YouTube expose per-message deletion, so archived provider IDs let Streamchat approximate bulk clearing. Plain `/clear twitch` instead uses Twitch's official clear-current-chat operation in one request. A time-qualified `/clear twitch Nd` uses known archived Twitch IDs but attempts only messages less than six hours old. YouTube clearing runs against the server archive and official `liveChatMessages.delete` endpoint. Messages Streamchat never observed cannot be cleared this way. `/clear` does not automatically remove messages from the local display, and neither `/clean` nor `/clear` deletes archive records.

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

When standard input and output are terminals, `streamchat run` uses the terminal's alternate screen with three fixed target-aware status lines at the top, scrolling chat in the middle, and a persistent single-line input bar at the bottom:

```text
Title:    Current stream title                         2026-08-15
Category: Just Chatting                                      16:07
Viewers:  42
-------------------------------------------------------------
chat
-------------------------------------------------------------
[KICK] >
```

The header follows the selected outbound target: `/kick` uses Kick status and `/twitch` combines Twitch `GET /helix/channels` title/category data with `GET /helix/streams` live/viewer data. A restored target selects its provider at startup. Target changes clear the old provider's data and trigger an immediate fetch; one generation-checked refresher prevents a slower response from the previous provider from overwriting the new selection. Status also refreshes every 30 seconds and after successful `/title` or `/category` commands. Offline streams show `Viewers:  OFFLINE` without inventing a count. With no status-capable target, all three values show `unavailable`; transient failures do not destroy the terminal UI.

The input starts as `[KICK] >` or `[Twitch] >` when that saved target is restored; otherwise it starts as `[NONE] >` and changes after `/kick` or `/twitch`. Chat rows use fixed provider, role, and nickname fields so `[KICK]`, `[Twitch]`, and `[YouTube]` all begin message text at the same terminal column. Twitch messages use compact, Twitch-like Unicode role symbols in broadcaster, moderator, partner, VIP, founder/OG, subscriber, and follower priority order. Kick and YouTube retain the provider-neutral `[B][M][P][V][O][S][F]` letter markers until their corresponding platform-specific checkpoints. Up to four roles are shown, all role types remain supported, and raw provider badges remain in the normalized message data. Incoming messages are confined between the fixed separators without discarding the current input or cursor position. `/clean` redraws only the chat region while retaining status, separators, and input. Basic Unicode insertion, Backspace, Left/Right, Home/End, Enter, Ctrl-C, and Ctrl-D are supported; long input scrolls horizontally to keep the cursor visible.

Chatter colors are assigned by first-seen order for each client session and are never persisted or taken from provider preferences. The fixed sequence is Cyan `#00D7D7`, Orange `#FF8700`, Magenta `#D75FD7`, Lime `#87D700`, Blue `#5F87FF`, Yellow `#FFD75F`, Violet `#875FFF`, Teal `#00AF87`, Red `#FF5F5F`, Mint `#5FFFAF`, Pink `#FF87AF`, and Sky `#5FD7FF`, wrapping to Cyan for the thirteenth new chatter. Provider user IDs retain assignments when available; otherwise platform plus case-insensitive username is used. The default `chat_color_mode` is `line`; `username` colors only the username, and `off` keeps the prior platform-only presentation. `--no-color` and `NO_COLOR` disable chatter colors.

```json
{
  "chat_color_mode": "line"
}
```

Slash commands have prefix-based, hierarchical completion in the interactive terminal. Type a prefix such as `/cl` to see matching commands, use Tab or Up/Down to select, Enter to accept an active suggestion, and Esc to dismiss the popup. Structured second tokens are suggested for commands such as `/clear`, `/ban`, `/timeout`, `/open`, and `/clean`; free-form titles, categories, usernames, durations, and chat text are not suggested. Completion never executes a command automatically.

Every exit path restores raw mode and the original shell screen. Piped/non-TTY input retains the line-oriented behavior without status fetching, alternate-screen, completion UI, or redraw sequences.

Kick emotes use their structured webhook metadata, and native Twitch emotes use the ordered fragments in `channel.chat.message`. Streamchat also loads the global and current-channel BetterTTV, FrankerFaceZ, and 7TV catalogues when Twitch connects, then recognizes exact whitespace-delimited names in Twitch messages. This catalogue lookup is best-effort and asynchronous: a slow or unavailable third-party service never delays or breaks Twitch chat, and an unavailable name simply remains readable text. Native Twitch ranges always take priority over third-party names.

In an interactive Kitty terminal with `emotes.mode` set to `auto`, Streamchat downloads the provider asset into its private cache and renders a native Kitty image in a three-column, one-row terminal slot. These images belong to the terminal grid and therefore scroll, resize, clear, and redraw with the chat; Streamchat does not start a helper process or create separate X11/Wayland windows. Until an asset is cached and accepted by Kitty, Kick emotes retain provider-supplied readable names such as `:ppJedi:` and Twitch emotes retain names such as `Kappa` or `KEKW`. Non-Kitty terminals use the same readable fallback automatically. Ordinary Unicode emoji remain ordinary terminal text.

```json
{
  "emotes": {
    "mode": "auto",
    "debug": false
  }
}
```

`auto` enables the Kitty-native backend when Streamchat detects Kitty. `text` always displays readable names and performs no graphical cache/backend work. `off` also disables graphical presentation while retaining the normalized provider metadata used by archives and relays. Existing cache data lives under `${XDG_CACHE_HOME:-$HOME/.cache}/streamchat/emotes/{kick,twitch}/`; changing presentation mode does not remove it. Emote presentation never rewrites or deletes archived message data.

Twitch fragment positions are converted to Streamchat's inclusive rune ranges, so CJK and Unicode emoji surrounding an emote do not shift its textual location. No per-message Helix lookup or scraped image URL is used. The Twitch asset resolver uses only the documented CDN template `https://static-cdn.jtvnw.net/emoticons/v2/{id}/{format}/dark/3.0`, selecting `animated` only when EventSub advertises it and otherwise using `static`. Animated provider assets use a deterministic first-frame PNG for the compact terminal preview.

The optional `emotes.debug` diagnostics describe cache and Kitty-backend activity without recording chat credentials or raw provider URLs. The platform-aware role symbols are terminal-safe approximations; downloading and drawing provider badge artwork is not part of this implementation.

## Server/client mode and archive

Run `streamchat serve` on the utility VM and let the interactive machine connect to it. The server receives verified Kick webhooks, discovers and streams the authenticated YouTube account's active live chat, and runs the configured Twitch EventSub WebSocket subscriber. Every accepted normalized event passes through the same SQLite-before-relay gate. There is no history replay. Kick and Twitch chat sends go directly from the interactive client to their official APIs using locally stored OAuth tokens; they do not pass through `/relay` and add no public endpoint.

```text
Kick → public VPS HTTPS reverse proxy ─┐
YouTube official API streamList ───────┼→ utility VM streamchat serve → SQLite
Twitch EventSub WebSocket ─────────────┘                 ↓ private WebSocket
                                                  main machine streamchat run
                                                         ↓ official chat APIs
                                                    Kick / Twitch
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

The browser flow uses the loopback callback `http://127.0.0.1:8791`, PKCE, and `youtube.force-ssl`, the narrower of Google's two documented scopes that permit live-chat sending and moderation. It requests offline access and stores the refresh token in the mode-`0600` config, allowing the service to refresh access without the Gentoo client. On a headless VM, use SSH port forwarding (`ssh -L 8791:127.0.0.1:8791 utility-vm`) and open the printed Google URL on your workstation. By default the server calls authenticated `liveBroadcasts.list` to find the active broadcast and its `snippet.liveChatId`; setting `youtube.video_id` retains an explicit broadcast/video override.

Google’s official `liveChatMessages.streamList` HTTP transport supplies live messages and a continuation token. Streamchat reconnects with that token after recoverable failures and waits between broadcasts so the service can remain running continuously.

To enable server-side Twitch ingestion, run `streamchat setup twitch --config /etc/streamchat/config.json`, authorize the five scopes listed in the Twitch setup table, and choose the channel. Write and moderation scopes are used only by the interactive client; `streamchat serve` remains read-only apart from chat ingestion. The server resolves the channel with `GET /helix/users`, subscribes to `channel.chat.message` version 1, follows EventSub reconnect URLs without creating a duplicate subscription, and keeps Kick/relay running if Twitch later stops with a terminal provider error.

Start it with:

```sh
streamchat serve --config /etc/streamchat/config.json
```

The default listen address is `127.0.0.1:8788`. An explicit loopback or private IP is accepted; wildcard and public-IP binds are rejected. Streamchat serves plain HTTP/WebSocket only. The VPS must terminate public TLS and forward only `/webhooks/kick` to the utility VM. Do not expose `/relay` through that public proxy. A private encrypted network such as WireGuard can carry `ws://` relay traffic directly.

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

The client sends the relay token only in the WebSocket `Authorization: Bearer` header, never in the URL. When a remote server URL is configured, `run` consumes relayed Kick, YouTube, and Twitch messages and does not start duplicate local adapters. The legacy standalone adapters remain available when no relay is configured.

Kick chatback and channel controls require the interactive machine's config to contain the Kick OAuth credentials and broadcaster ID created by `streamchat setup kick`. Twitch chatback, status, and channel controls similarly require `streamchat setup twitch` in the interactive machine's config. Provider credentials are used only for direct official API requests and are never sent to the Streamchat server or relay. The headless server never changes a Twitch title or category.

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

For the utility server, `streamchat setup youtube-server` performs Google's official desktop OAuth authorization-code flow with PKCE and offline access. It requests `youtube.force-ssl`, stores and refreshes the access/refresh tokens, discovers the authenticated account's active broadcast through `liveBroadcasts.list`, and consumes the preferred lower-latency `liveChatMessages.streamList` endpoint. The same authorization can be used for official live-chat sending and moderation operations when those controls are connected to the shared runtime.

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

Streamchat requests exactly `user:read:chat`, `user:write:chat`, `channel:manage:broadcast`, `moderator:manage:banned_users`, and `moderator:manage:chat_messages`. These authorize EventSub chat reads, chat sends, the authenticated broadcaster's title/category changes, ban/timeouts, and remote chat clearing respectively. Existing authorizations remain valid for the features covered by their scopes; missing moderation scopes do not break chat, status, or channel controls. Streamchat validates tokens, stores rotated refresh tokens, and resolves moderation targets through the official `GET /helix/users` API.

Twitch ban and timeout use `POST /helix/moderation/bans` with the configured channel as `broadcaster_id` and the authenticated account as `moderator_id`. Twitch timeouts accept whole seconds from `1s` through `14d`; Kick keeps its existing whole-minute rules. Plain `/clear twitch` sends one `DELETE /helix/moderation/chat` request without a message ID, which clears current Twitch chat without changing Streamchat's local display or archive. `/clear twitch Nd` reads known Twitch IDs from the SQLite archive and attempts individual deletion only for events less than six hours old. Older rows are reported as platform-limited and all archive rows remain historical.

The reused adapter handles welcome, notification, keepalive, reconnect, revocation, duplicate delivery, and shutdown. Server mode sends its normalized messages through the same archive/relay channel as Kick and YouTube; relay clients do not start another local Twitch subscriber. Before connecting, Streamchat validates the token and refreshes and atomically persists it when required. Twitch broadcaster, moderator, partner, VIP, and subscriber badges map only when supplied by Twitch. One serialized interactive user-token client now shares refresh, persistence, and one-retry behavior across chat sends, status reads, and channel controls. Twitch requires the `broadcaster_id` on `PATCH /helix/channels` to equal the user ID in the access token; Streamchat rejects a known mismatch locally and does not claim moderator ownership. Errors report authentication, missing scope, ownership, rate-limit, rejection, and network failures without exposing credentials.

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

The core server status service reports the dimensions and incoming bitrate of the encoded video that actually reaches the VPS, together with the canonical stream title and category obtained from Streamchat's channel-status providers. It runs `ffprobe` against the private MediaMTX input every 15 seconds and publishes versioned, sanitized state through the authenticated `/api/status` endpoint. The bot console is currently one consumer; the CLI, GUI, and future appliance can use the same API. `bot.stream_probe_url_env` names the protected service environment variable containing that input URL (by default `STREAMCHAT_MEDIA_INPUT_URL`); the URL and any MediaMTX reader credentials are therefore not stored in Streamchat's JSON configuration or returned by its API. When `STREAMCHAT_MEDIAMTX_METRICS_URL` is set, the same probe samples MediaMTX's `paths_inbound_bytes` counter for `bot.media_path` and reports the measured stream-connection rate. It never counts unrelated host traffic. MediaMTX is authoritative for media properties—not the OBS canvas or delayed platform dashboards—while Streamchat's platform APIs are authoritative for title and category.

The intended bot is an event-driven automation system, not only a command responder. Platform adapters will normalize chat, follows, subscriptions, gifts, raids, paid interactions, rewards, moderation, stream lifecycle, clock and manual events. Durable rules will map those events to chat messages, delayed message sequences, alerts, media, archives, dashboard notifications, safe internal utilities and optional conversational replies. The Kick/Twitch `!commands` rule and a deduplicated Discord live notification are executable today; the other dashboard areas are honest placeholders. The Discord application is named `streamchat-bot`, while its visible bot name may be `ComradeKip`. Its token stays in `STREAMCHAT_DISCORD_BOT_TOKEN`; the separate MediaMTX hook secret stays in `STREAMCHAT_MEDIA_HOOK_TOKEN`; only their environment-variable names, the destination channel ID and transition-confirmation count belong in JSON. A session begins when the Tokyo MediaMTX relay reports that the continuous OBS `pc/stream` input is ready. Camera inputs are deliberately ignored, so GoPro battery swaps, mobile-network loss and short high-altitude disruptions do not create duplicate announcements while OBS remains connected. At the start of a newly confirmed session, Streamchat refreshes all three platform statuses and sends one `@everyone` notification containing one clickable URL for every output it can verify as live. Discord mention parsing is restricted to `@everyone`; user and role mentions in any other text stay inert. Run `streamchat bot discord-test` for a non-pinging delivery test. See [`docs/bot-architecture.md`](docs/bot-architecture.md) for the complete working/planned boundary and intended runtime model.

Executed bot commands have a separate, deliberately narrow journal at `bot.command_log_path` (default `/var/lib/streamchat/bot-commands.jsonl`). Its mode-`0600` JSONL records contain only UTC time, platform, canonical command name and success state. Ordinary chat, identities, channel IDs, command arguments and replies, provider error text, Discord notifications, subtitle activity and stream lifecycle events are never written there. Unknown and cooldown-suppressed messages are not executions and are not recorded.

The server also provides a transparent, read-only OBS chat overlay at
`/overlay/chat`. For example, a private WireGuard deployment listening on
`10.77.0.1:8793` can be added to OBS as a browser source using:

```text
http://10.77.0.1:8793/overlay/chat?limit=10&ttl=120
```

`limit` controls the number of visible messages (1-20) and `ttl` controls how
many seconds a message remains visible (15-900). The overlay normalizes Kick,
Twitch and YouTube chat, wraps long text, identifies the source platform, and
omits moderation and system events. Its page, stylesheet, script and chat-feed
endpoint are intentionally available without the administration password so
an OBS browser source can read them. They expose no controls or credentials;
bind a non-loopback console only to a trusted private interface and keep every
administration endpoint password-protected.

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
- Chatter colors: `STREAMCHAT_CHAT_COLOR_MODE` (`line`, `username`, or `off`)
- Discord bot: `STREAMCHAT_DISCORD_BOT_TOKEN` (secret; used only when `bot.discord.enabled` is true)
- MediaMTX lifecycle hook: `STREAMCHAT_MEDIA_HOOK_TOKEN` (separate secret; authenticates only OBS ready/not-ready events)

See `examples/config.example.json` for the manual JSON shape. Environment-provided access tokens are used in memory and are not written back during refresh.

## Architecture

- `internal/chat`: normalized credential-free message and adapter contract
- `internal/platform`: registry/factory and platform selection
- `internal/platform/youtube`: official Data/Live Streaming API adapter
- `internal/platform/kick`: OAuth, chat sending, channel controls, subscriptions, verified webhook receiver
- `internal/outbound`: canonical outbound target selection and command aliases
- `internal/clientruntime`: frontend-neutral sending, control, setup, health, and archive operations
- `internal/gui`: private loopback HTTP/SSE bridge shared by browser development and the native frontend
- `internal/bot` and `internal/botgui`: provider-neutral event/rule foundation, current always-on command engine, and separately bound private administration console
- `internal/streamprobe`: cached VPS-local MediaMTX media-property probe
- `internal/serverstatus`: versioned shared server telemetry consumed by CLI, GUI, bot, and appliance clients
- `internal/clientstate`: atomic, non-secret interactive client preferences
- `internal/platform/twitch`: OAuth/API support and EventSub WebSocket adapter
- `internal/relay`: authenticated WebSocket chat relay plus the shared `/api/status` endpoint
- `internal/archive`: versioned SQLite storage and operational statistics
- `internal/setup`: interactive, safely rerunnable setup wizard
- `internal/config`: JSON/env/defaults, atomic writer, validation, redaction
- `internal/aggregate`: bounded chronological merge and duplicate cache
- `internal/render`: terminal frontend and injection defense
- `internal/emote`: provider-neutral fallback formatting, persistent asset cache, and optional image backend
- `internal/terminalui`: raw-mode key editing and synchronized persistent input bar
- `internal/logging`: opt-in private JSONL writer
- `cmd/streamchat`: human CLI, lifecycle, and signal handling
- `cmd/streamchat-gui`: private native-frontend runtime and optional built-in local server
- `desktop`: native Qt 6 frontend

JSONL logging is disabled by default and files are mode `0600`. Chat logs can contain personal data. Terminal output strips control sequences from untrusted platform content.

## Development

```sh
make check
```

Tests use fake adapters, `httptest` servers, invented fixtures, and fake WebSockets. They do not call external services.

## Debian and Ubuntu packages

Streamchat uses the following distribution model:

- Gentoo provides one `streamchat` package with the existing `cli`, `server`, `gui`, and `bot` USE flags.
- Ubuntu and Debian provide separate `streamchat-cli`, `streamchat-server`, and `streamchat-gui` packages.
- Windows provides the GUI with its optional built-in local server. The CLI is not packaged for Windows.
- Manual builds may select whichever components are needed.

The general Linux and container component model consists of `cli`, `server`, `gui`, and `bot`. A later packaging pass may map all four components more rigorously across Debian/Ubuntu, Arch Linux, and Docker; the exact split is deliberately left open for now.

Ubuntu 24.04 is the canonical Debian-package build and validation target. A move to Ubuntu 26.04 is planned around March 2027, with packages, services, VMs, and deployment paths retested as part of that migration.

Build all three Ubuntu/Debian packages with the locally installed Go toolchain, Qt 6 development files, and `dpkg-deb`:

```sh
VERSION=3.2 make deb
sudo apt install \
  ./dist/streamchat-cli_3.2_amd64.deb \
  ./dist/streamchat-server_3.2_amd64.deb \
  ./dist/streamchat-gui_3.2_amd64.deb
```

Without `VERSION`, builds use an exact Git tag or a development value containing the commit date and hash. The package builds inject the upstream version into the binaries, so this release reports `streamchat 3.2`.

`streamchat-cli` owns the statically linked shared runtime and `/usr/bin/streamchat`. It explicitly replaces the legacy combined `streamchat` package during upgrades. `streamchat-server` depends on the exact same CLI version and owns `streamchat-server.service`. `streamchat-gui` depends on the exact same CLI version and provides the native Qt application. Install a headless server with these two local artifacts:

```sh
sudo apt install \
  ./dist/streamchat-cli_3.2_amd64.deb \
  ./dist/streamchat-server_3.2_amd64.deb
```

The server package creates the dedicated `streamchat` account and `/etc/streamchat` only when missing. It never removes or replaces `/etc/streamchat/config.json` or `/var/lib/streamchat/streamchat.db`. On a fresh server, install the example privately, review it, then enable the service:

```sh
sudo install -m 0600 -o streamchat -g streamchat \
  /usr/share/doc/streamchat-server/examples/config.example.json \
  /etc/streamchat/config.json
sudo systemctl enable --now streamchat-server.service
```

During an upgrade, the package detects an enabled or active legacy `streamchat.service`, stops and disables it, reloads systemd, and transfers the corresponding enabled/active state to `streamchat-server.service`. The new unit also conflicts with the old service so both cannot run together. The old manually installed unit file is left untouched and may be removed after the new service is verified; configuration and archive data are retained even if the package is removed.

## License

MIT. See `LICENSE`.
