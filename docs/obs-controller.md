# OBS phone controller

## Phone interface restored — 2026-10-25

The phone entry point is http://10.77.0.1:8788/obs/ over WireGuard. The controller
no longer requires a browser access token, per user instruction. Requests must
arrive from 10.77.0.0/24 with Host 10.77.0.1:<port>; foreign Origin headers are
rejected. Windows agent and OBS credentials remain protected server-side.

The original purple theme and three swipeable pages are retained:
- Control: Windows OBS launch/normal close, stream/record controls, scenes,
  remote subtitles, and GPS layout. Windows must be awake with hifro logged in.
  Close refuses active or unknown stream, recording, virtual-camera, replay or
  plugin output states. Start selects Streamchat Main / Streamchat 1080p60 /
  Starting Soon and does not start outputs.
- Stream: actual Tokyo relay-process status for Twitch, Kick and YouTube,
  combined recent chat, and the requested "Live feed — not ready yet" placeholder.
- Banana: monitoring and allowlisted recovery, when a monitoring agent is
  configured and reachable. The legacy handoff confirms that agent deployment
  had been deferred; an unconfigured/offline router is shown honestly.

Windows Speedify and local subtitle mode are omitted by user request.
STREAMCHAT_OBS_RELAY_STATUS_URL supplies the private relay status endpoint.
Optional STREAMCHAT_OBS_BANANA_URL and STREAMCHAT_OBS_BANANA_TOKEN_FILE configure
Banana's existing monitoring/recovery protocol. Do not invent router endpoints
or expose backend tokens to the browser.

## Backend configuration

Set STREAMCHAT_OBS_URL to the Windows OBS websocket and
STREAMCHAT_OBS_PASSWORD_FILE to its protected password file. Set
STREAMCHAT_OBS_AGENT_URL and STREAMCHAT_OBS_AGENT_TOKEN_FILE for the Windows
companion. The service serves the phone interface under /obs/ and its phone
API under /obs/phone/. Keep the service bound to the private WireGuard listener.

The companion runs as hifro through the Streamchat-OBS-Agent logon task. Its
private config retains Listen, TokenFile, OBSURL, OBSPasswordFile, BotURL,
BotPasswordFile, Root, FFmpeg, Cava, CavaConfig and Microphones. It supplies
GPS on loopback8765, CAVA on8796, and writes subtitle text under Root/subtitles.
Only allowlisted microphone endpoints are used; no automatic default-device
fallback. Missing microphones are rejected before starting a paid worker.

Phone mutations are explicit and never retried automatically. After a timeout,
inspect current state before retrying. The phone interface confirms starting or
stopping stream/recording and closing OBS. Launch and close act only on the
known Windows OBS installation; closing is graceful and never forced.

Do not run public streaming commands for verification. Use an idle close/start
cycle and a short private recording to check the close guard.

## Subtitle capture recovery

The Windows companion detects ten seconds without PCM frames and restarts audio
capture after a three-second delay, reusing the existing subtitle worker. Silent
PCM frames count as healthy capture. Stopping subtitles cancels pending retries.
This handles capture-process failures; it does not prove or repair every possible
remote recognition stall.
