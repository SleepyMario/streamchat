# Streamchat bot architecture

## Status boundary

The executable automation today consists of the deterministic, centrally implemented `!commands` and `!language` replies on Kick and Twitch plus one Discord live notification. Separate bot identities, per-platform enablement, cooldowns, test replies, the activity log, optional recent-chat display and the shared status foundation are implemented. Recognized chat commands that actually execute are also appended to the private mode-`0600` JSONL file configured by `bot.command_log_path` (default `/var/lib/streamchat/bot-commands.jsonl`). Each record contains only its UTC time, platform, canonical command name and success state. It deliberately excludes ordinary chat, identities, channel IDs, command arguments and replies, provider errors, Discord announcements, subtitle activity and stream lifecycle events. Unknown or cooldown-suppressed messages are not command executions and are not recorded.

The Discord notification is driven by authenticated MediaMTX ready/not-ready events for the continuous OBS input at `pc/stream` on the Tokyo relay. GoPro and other camera-input changes are deliberately excluded: battery swaps, brief mobile-network loss and short high-altitude disruptions therefore cannot create duplicate announcements while OBS remains connected. Two consecutive samples confirm each signalled state. Starting Streamchat while OBS is already connected establishes a baseline without sending a duplicate; a notification is re-armed only after a confirmed OBS disconnect. For a new session, the shared runtime refreshes Twitch, Kick and YouTube status and resolves each confirmed-live public URL. One message contains one `@everyone`, the names of all detected live outputs, and one clickable URL per output. If no live output with a public URL is ready yet, the watcher waits and retries instead of posting a false or empty announcement. `streamchat bot discord-test` sends a fixed non-pinging delivery test without starting a public stream.

The Discord application is technically named `streamchat-bot`; its public bot identity is `ComradeKip`. It needs only the `bot` installation scope and the View Channels, Send Messages and Mention Everyone permissions in the designated channel. No privileged gateway intent is required for this one-way notification. Discord's `allowed_mentions` payload permits only `everyone`, so stream titles and future configurable text cannot accidentally ping users or other roles. The channel ID lives under `bot.discord` in JSON. The legacy `message` setting remains accepted for configuration compatibility but live announcements are generated from current platform state. The bot token is read only from the environment variable named by `bot.discord.token_env` (default `STREAMCHAT_DISCORD_BOT_TOKEN`). A separate least-privilege MediaMTX hook secret is read from `bot.discord.media_hook_token_env` (default `STREAMCHAT_MEDIA_HOOK_TOKEN`). Neither secret may be stored in JSON, logs, Git, or command history.

Twitch bot replies may use a dedicated account instead of the broadcaster identity. `streamchat setup twitch-bot` authorizes that account with only `user:read:chat` and `user:write:chat`, resolves its Twitch user ID/login, and stores it under `bot.twitch` without replacing the primary channel-owner authorization. The channel target continues to come from `twitch.channel`. The existing Twitch application may be reused; its Client ID and secret are copied into the dedicated account record so token refresh remains self-contained. Production configuration must remain mode `0600` and must never be printed without redaction. The fixed, case-insensitive 3.4 commands are `!commands` and `!language`; both enter the command-only journal when executed. Duplicate suppression is tracked independently per platform, channel and command. The same command may run again after the configured cooldown (60 seconds by default) or after four ordinary intervening chat messages, while a different command runs immediately and alert events never enter this command cooldown.

Kick bot replies may likewise use a dedicated account. `streamchat setup kick-bot` authorizes it with only `user:read` and `chat:write`, resolves its Kick user ID/name, and stores it under `bot.kick`. The channel target remains the primary `kick.broadcaster_id`; the primary OAuth tokens, webhook and event subscriptions are not changed. Kick and Twitch use the same central command definitions and cooldown implementation rather than platform-specific copies.

Streamchat 3.6 presents normalized Kick broadcaster, moderator, partner, VIP, OG, subscriber and follower roles with compact Kick-aware Unicode markers in terminal clients. This is presentation only: normalized roles and raw provider badge records remain unchanged for relays, archives and graphical clients.

Streamchat 3.7 subscribes the primary Kick identity to `channel.followed`, `channel.subscription.new`, `channel.subscription.renewal`, `channel.subscription.gifts` and `kicks.gifted` alongside `chat.message.sent`. The signed webhook receiver converts them into the shared follow, subscription, gift-subscription and donation event model before relay, archive and bot evaluation. ComradeKip acknowledges enabled Kick events in Kick chat using the same fixed named/count-aware wording model as Twitch. These alerts do not enter the command cooldown or command-only journal.

Streamchat 3.4 receives basic Twitch follow, new-subscription and aggregate gift-subscription EventSub notifications and recognizes Bits in structured Twitch chat events. These events enter the normal Streamchat relay/archive once, and the dedicated Twitch bot posts a fixed, simple acknowledgement in Twitch chat. Aggregate gifts name the gifter and count when Twitch supplies them; anonymous gifts remain anonymous. Alert toggles and customizable wording remain deliberately deferred. Arbitrary rules, timed messages, message sequences, overlays, conversational AI, recording control, clips, YouTube bot actions and PeerTube integration are **planned architecture**, not working features. Their dashboard controls remain disabled or absent until the corresponding adapter and runtime have real tests.

The bounded path to the next packaged release is: 3.2 dedicated Twitch bot replies; 3.3 Twitch-specific role/badge emojis; 3.4 basic Twitch follow, subscription, gift-subscription and Bits alerts in chat plus the fixed `!language` command; 3.5 Kick bot replies with `!commands` and the same fixed `!language` command; 3.6 Kick-specific role/badge emojis; 3.7 basic Kick alerts in chat, including names and gift counts when Kick supplies them; 3.8 YouTube bot replies with `!commands` and the same fixed `!language` command; 3.9 YouTube-specific role/badge emojis; 3.9.1 cross-platform verification that every manual command is shared across all available platforms, with unification only if that audit finds fragmentation; 3.9.2 a broadcaster-marker consistency pass that changes Twitch from red to the same green marker used by Kick and ensures YouTube uses it too; and 4.0 YouTube bot feature parity, including names and gift-membership counts when YouTube supplies them. Versions 3.2 through 3.9.2 are tested source checkpoints only. Docker, stable Gentoo, Ubuntu/Debian and Windows packaging resumes for 4.0. After 4.0, Streamchat enters maintenance mode for the following weeks apart from bug fixes and required provider changes.

## GPU subtitle sessions

Streamchat-bot owns the control plane for temporary RunPod subtitle workers. When explicitly enabled, its authenticated API can create one Secure Cloud worker from an ordered inexpensive-GPU preference list, issue a fresh random worker token, report readiness and price, accept controller heartbeats, and permanently delete the worker. It rejects a second concurrent creation, refuses a worker above the configured hourly price ceiling, stores recovery state privately, deletes recovered orphan state after a service restart, and enforces both a heartbeat timeout and a six-hour default maximum lifetime. The permanent RunPod key is read from `RUNPOD_API_KEY` (or the configured environment-variable name), never from JSON.

The Slacktop phone controller is the data-plane coordinator. It starts the bot-owned worker, launches the local microphone sender, sends heartbeats, and stops both parts. Microphone audio travels directly from Slacktop to the authenticated RunPod WebSocket; it never passes through the VPS or phone browser. The worker returns original-language text, an English translation, and a Simplified Chinese translation. Slacktop writes those to separate local OBS text files and a combined display file. English and Mandarin originals suppress the corresponding duplicate translation, leaving two lines instead of three. The accepted original languages are English, Dutch, German, Simplified Mandarin Chinese, Korean, Vietnamese and Japanese.

This is deliberately session-based. It has no persistent RunPod volume, does not place secrets in the public image, and does not start a paid worker merely because Streamchat starts. Production deployment and a real live-stream acceptance test remain separate gates.

## Data flow

```text
Kick / Twitch / YouTube / PeerTube / MediaMTX / manual control / clock
                               |
                               v
                    provider-specific adapters
                               |
                               v
                 credential-free normalized events
                               |
                               v
          persistent rule, schedule and cooldown evaluation
                               |
                               v
            deterministic actions or optional AI action
                               |
                               v
 chat send / alert / media / archive / dashboard / safe core utility
```

Streamchat has three server-owned information surfaces:

1. **Current state** — channel title/category/viewers, media dimensions, encoded bitrate and stream-only connection rate.
2. **Events** — chat, follows, subscriptions, gifts, raids, donations, rewards, moderation, stream lifecycle, schedules and manual triggers.
3. **Automation state** — configured rules, enabled state, last/next execution, cooldowns, failures and an audit trail.

The server is authoritative. The CLI, native GUI, bot console and any future monitor/appliance are consumers; none should independently reinterpret platform payloads.

## Rules and actions

Every rule has a stable ID, name, enable switch, normalized trigger, platform scope, conditions, ordered actions, cooldown, deduplication window, priority, optional validity window and retry policy. Scheduled rules additionally carry a timezone and one-shot, fixed-time, interval or relative-to-stream-start mode. Scheduler state must eventually be persisted so a process restart cannot silently lose work or duplicate a message.

Actions include a single chat message, a delayed message sequence, visual alert, sound/media cue, archive entry, dashboard notification, allowlisted internal utility and an optional conversational response. Mutating utilities—moderation, title changes, recording and similar operations—require separate explicit permissions and remain disabled by default.

## Conversational bot

Conversational behavior is an optional action within the same automation engine, not a second bot. The first safe mode should be mention-only with strict per-platform rate limits and bounded recent-chat context. Later modes may answer direct questions or participate autonomously.

The model provider must be replaceable through an OpenAI-compatible or local endpoint. Secrets live outside ordinary JSON configuration. Chat is untrusted conversation data, never system instructions. The model cannot reveal credentials, change its own policy, execute arbitrary commands or invoke mutating tools without an independently configured allowlist. Every generated reply records its trigger, rule, provider/model identity, outcome and safe summary in the audit stream.

## Alert and scheduling behavior

Platform-native follows, subscriptions/memberships, gifts, raids, donations/paid messages, rewards and moderation events map to shared event kinds while retaining safe provider metadata. Rules may batch bursts, delay replies, send several messages with different delays, run only while live or fire once and remove themselves. Every integration needs a simulation/test event so it can be checked without requiring a real purchase, follow or raid.

## Hardware boundary

The dashboard remains a normal responsive web client. A future Alpine, Ubuntu Core or other kiosk-style monitor appliance is optional and must not influence the server protocol or feature design.
