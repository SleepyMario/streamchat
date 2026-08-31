# Streamchat bot architecture

## Status boundary

The executable automation today consists of the deterministic `!commands` reply on Kick and Twitch plus one Discord live notification. Separate bot identities, per-platform enablement, cooldowns, test replies, the activity log, optional recent-chat display and the shared status foundation are implemented.

The Discord notification watches the authoritative VPS media probe. Two consecutive probe results must confirm a state change. Starting Streamchat while a broadcast is already live establishes a baseline without sending a duplicate; a notification is re-armed only after a confirmed offline state. For a new session, the shared runtime refreshes Twitch, Kick and YouTube status and resolves each confirmed-live public URL. One message contains one `@everyone`, the names of all detected live outputs, and one clickable URL per output. If no live output with a public URL is ready yet, the watcher waits and retries instead of posting a false or empty announcement. `streamchat bot discord-test` sends a fixed non-pinging delivery test without starting a public stream.

The Discord application is technically named `streamchat-bot`; its public bot identity is `ComradeKip`. It needs only the `bot` installation scope and the View Channels, Send Messages and Mention Everyone permissions in the designated channel. No privileged gateway intent is required for this one-way notification. Discord's `allowed_mentions` payload permits only `everyone`, so stream titles and future configurable text cannot accidentally ping users or other roles. The channel ID lives under `bot.discord` in JSON. The legacy `message` setting remains accepted for configuration compatibility but live announcements are generated from current platform state. The bot token is read only from the environment variable named by `bot.discord.token_env` (default `STREAMCHAT_DISCORD_BOT_TOKEN`) and must not be stored in JSON, logs, Git, or command history.

Follow/subscription alerts, arbitrary rules, timed messages, message sequences, overlays, conversational AI, recording control, subtitles, clips, YouTube bot actions and PeerTube integration are **planned architecture**, not working features. Their dashboard controls remain disabled or absent until the corresponding adapter and runtime have real tests.

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
