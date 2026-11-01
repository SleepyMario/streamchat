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
  combined recent chat, and an opt-in Twitch viewer on the current HTTPS interface.
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

## Independent bitrate buttons — 2026-11-01

The phone/tablet controller has 5,000, 6,000 and 8,000 kbps buttons, independent of scene selection and stream start/stop. POST `/obs/phone/api/stream/bitrate?kbps=6000` applies a selection; GET reads the OBS helper heartbeat and encoder target. No automatic scene/profile selection occurs. Existing stream/record confirmations remain.

Load `scripts/streamchat-bitrate.lua` in OBS Tools > Scripts for each scene collection used with the controller. The script exchanges expiring, uniquely identified requests and acknowledgments through the existing authenticated OBS websocket profile-parameter API. No new network listener or credentials are needed. It changes only the streaming encoder bitrate and persists streamEncoder.json using OBS's safe-save API. It never starts, stops, or restarts a stream, recording, or scene. Advanced CBR output is required; unsupported dynamic encoders, multiple encoders, automatic dynamic bitrate, or shared active recording/replay encoders are rejected. The displayed live rate is the encoder target, not measured network throughput. A timeout is not automatically retried.

The earlier automatic IRL/gaming profile-selection design is superseded. Existing profiles remain usable, but the controller no longer switches them. Verify with an isolated private output; never start a public stream for testing.

## Private HTTPS Twitch viewer — 2026-11-01

https://sleepymario.com/obs/ serves the canonical controller HTML from nginx, with the API proxied to the existing WireGuard listener. The nginx locations allow the WireGuard IPv4 subnet 10.77.0.0/24 and the exact private IPv6 addresses of the phone (fd77:6d61:7269::5/128) and tablet (fd77:6d61:7269::7/128), ignoring forwarded-IP headers; public access fails closed. Clients route both 63.250.52.84/32 and 2602:ff16:14:1086::1/128 through WireGuard when using the public DNS name. The server assigns fd77:6d61:7269::1/64 to wg0 and authorizes each device IPv6 /128 on its existing peer. Preserve existing IPv4 routes and keys. Reconnect the client tunnel fully after adding IPv6; merely refreshing Firefox may leave old network state. Twitch video goes directly to the device. The static HTML deployment avoids restarting chat ingestion; deploy internal/obscontrol/index.html to /var/www/obs-controller/index.html alongside scripts/obs-controller-nginx.conf. The original HTTP endpoint remains available with its previous embedded HTML until the next binary release.

The viewer is opt-in, muted, supports Twitch fullscreen/quality controls, and unloads on close, tab navigation, page hide or backgrounding. Its state says Player open, not Live; Twitch displays actual playback/offline status. Minimum 400x300 player size is preserved; narrow phones can scroll horizontally or use landscape/fullscreen. Kick and YouTube viewers remain deferred.


## Camera playback recovery — 2026-11-01

The Windows companion monitors local MediaMTX paths `irl-vps` for `IRL - VPS` / `VPS-MMTX` and `irl` for `IRL - Banana` / `Banana-MMTX`. While unavailable, the camera source is hidden so the existing fallback remains visible. Once ready, it enables the source before restarting playback. A changed `readyTime` identifies a replacement feed session, including reconnects between polls. Error, ended, stopped and none media states trigger retries with a 15-second cooldown. No program-scene switch or output start/stop is performed.

Expected GoPro battery changes can interrupt longer IRL/sports streams for 2–5 minutes, with flexible timing (for example between hours one and two of a planned 2.5-hour stream). Recovery waits for input rather than requiring manual OBS intervention. It cannot restore an offline camera publisher or detect every frozen picture reported as playing. Source visibility is automatically managed; manually hiding a ready camera source is not a persistent override.

Unit tests cover recovery, session replacement and cooldowns. The opt-in `STREAMCHAT_IRL_LIVE_TEST=1` test requires idle OBS on Windows VM700 and creates a temporary synthetic local-video source; it tests real OBS playback with simulated feed-state changes, not a real GoPro/network outage. Do not enable it during a live stream. A real GoPro reconnect and live Twitch playback remain acceptance tests.

## Deployment verification — 2026-11-01

Both phone and tablet loaded the HTTPS controller and its status APIs over WireGuard. Direct IPv4 and IPv6 HTTPS checks returned 200; public controller access returned 403 while the public blog remained accessible. Both Twitch embeds displayed the expected offline-channel message. These checks do not establish live video playback. The legacy `viewer_available` monitor flag describes the retired Tokyo return viewer, not the Twitch embed; it must not be used to infer Twitch availability. The observed input-ready/log discrepancy remains a separate monitoring issue.
