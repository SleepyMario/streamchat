package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SleepyMario/streamchat/internal/aggregate"
	archivepkg "github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/chattercolor"
	"github.com/SleepyMario/streamchat/internal/clientstate"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/emote"
	"github.com/SleepyMario/streamchat/internal/launcher"
	"github.com/SleepyMario/streamchat/internal/logging"
	"github.com/SleepyMario/streamchat/internal/outbound"
	platformreg "github.com/SleepyMario/streamchat/internal/platform"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
	"github.com/SleepyMario/streamchat/internal/relay"
	"github.com/SleepyMario/streamchat/internal/render"
	"github.com/SleepyMario/streamchat/internal/setup"
	"github.com/SleepyMario/streamchat/internal/terminalui"
)

const version = "0.3.0"
const statusRefreshInterval = 30 * time.Second
const usage = `Streamchat reads and merges live chat from YouTube, Kick, and Twitch.

Start here:
  streamchat setup                 Configure one or more services interactively
  streamchat run                   Read all configured chat targets

Server/client mode:
  streamchat serve                 Ingest, archive, and relay Kick/YouTube chat
  streamchat run                   Connect to the configured Streamchat server

Interactive commands:
  /kick                            Select Kick as the outbound target
  /kick hello                      Select Kick and send "hello"
  hello                            Send to the selected target
  /title New stream title          Update the Kick stream title
  /category Just Chatting          Update the Kick stream category
  /ban kick USER                   Permanently ban a user from Kick chat
  /timeout kick USER 10m           Temporarily timeout a user from Kick chat
  /clean streamchat                Clear the current local chat view
  /clean kick                      Hide displayed Kick messages locally
  /clean USER                      Hide a user's displayed messages locally
  /clear kick                      Delete archived Kick messages from last 24h
  /clear kick 3d                   Use archived Kick messages from last 3 days
  /open kick                       Open Kick in mpv or the default browser
  /open youtube                    Open the configured YouTube stream
  /open twitch                     Open the configured Twitch channel
  /exit                            Exit the interactive client cleanly
  /quit                            Same as /exit
The last selected outbound target is restored on the next run when available.
Title, category, and moderation commands currently target Kick only.
/clean kick is local-only; /clear kick uses archived IDs for remote deletion.
Neither command deletes Streamchat archive records.
On a terminal, the alternate screen keeps Kick status at top and input at bottom.

Useful commands:
  streamchat setup youtube|kick|twitch [--config PATH]
  streamchat setup youtube-server  Authorize unattended server ingestion
  streamchat run --youtube-video URL_OR_ID
  streamchat run --twitch-channel CHANNEL_OR_URL
  streamchat config show           Show configuration with secrets redacted
  streamchat config check          Check configuration and recovery steps
  streamchat archive stats         Show SQLite archive status
  streamchat demo                  Run an offline demonstration

Advanced compatibility commands:
  streamchat youtube --youtube-video URL_OR_ID
  streamchat kick serve
  streamchat kick subscribe [--inspect]

Configuration precedence: command-line flags override STREAMCHAT_* environment
variables, which override the JSON file, which overrides safe defaults.
Default file: ${XDG_CONFIG_HOME:-$HOME/.config}/streamchat/config.json
`

func main() { os.Exit(runWithInput(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
func run(args []string, out, errw io.Writer) int {
	return runWithInput(args, strings.NewReader(""), out, errw)
}

func runWithInput(args []string, in io.Reader, out, errw io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 {
		c, e := config.Load("")
		if e == nil {
			config.ApplyEnv(&c, os.Getenv)
		}
		if e == nil && !c.HasUsablePlatform() {
			w := setup.New(in, out, "")
			e = w.Run(ctx, nil)
			return finish(e, errw)
		}
		fmt.Fprint(out, usage)
		return 0
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(out, usage)
		return 0
	}
	var e error
	switch args[0] {
	case "version":
		fmt.Fprintf(out, "streamchat %s\n", version)
		return 0
	case "setup":
		var selected []string
		var setupPath string
		selected, setupPath, e = setupArguments(args[1:])
		if e == nil {
			e = setup.New(in, out, setupPath).Run(ctx, selected)
		}
	case "demo":
		e = demo(ctx, args[1:], out)
	case "youtube":
		e = runPlatforms(ctx, "youtube", args[1:], in, out, errw)
	case "run":
		e = runPlatforms(ctx, "run", args[1:], in, out, errw)
	case "serve":
		e = serve(ctx, args[1:], out)
	case "archive":
		if len(args) < 2 || args[1] != "stats" {
			e = errors.New("choose 'archive stats'")
		} else {
			e = archiveStats(ctx, args[2:], out)
		}
	case "kick":
		if len(args) < 2 {
			e = errors.New("choose 'kick serve' or 'kick subscribe'")
		} else if args[1] == "serve" {
			e = runPlatforms(ctx, "kick", args[2:], in, out, errw)
		} else if args[1] == "subscribe" {
			e = subscribe(ctx, args[2:], out)
		} else {
			e = errors.New("unknown Kick command")
		}
	case "config":
		if len(args) < 2 {
			e = errors.New("choose 'config show' or 'config check'")
		} else if args[1] == "show" {
			e = showConfig(args[2:], out)
		} else if args[1] == "check" {
			e = check(args[2:], out)
		} else {
			e = errors.New("unknown config command")
		}
	default:
		e = errors.New("unknown command; run 'streamchat --help'")
	}
	return finish(e, errw)
}

func setupArguments(args []string) ([]string, string, error) {
	platformArgs := make([]string, 0, 1)
	var path string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config":
			if i+1 >= len(args) {
				return nil, "", errors.New("--config needs a path")
			}
			i++
			path = args[i]
		case strings.HasPrefix(args[i], "--config="):
			path = strings.TrimPrefix(args[i], "--config=")
		default:
			platformArgs = append(platformArgs, args[i])
		}
	}
	selected, err := setup.ParsePlatforms(platformArgs)
	return selected, path, err
}

func finish(e error, errw io.Writer) int {
	if e == nil {
		return 0
	}
	if errors.Is(e, setup.ErrCancelled) || errors.Is(e, context.Canceled) {
		fmt.Fprintln(errw, "Setup cancelled; existing configuration was not changed.")
		return 130
	}
	fmt.Fprintf(errw, "streamchat: %s\n", safeError(e))
	var ae *chat.AdapterError
	if errors.As(e, &ae) && ae.Kind == chat.Authentication {
		return 4
	}
	return 2
}

type opts struct {
	config, log, video, key, token, broadcaster, listen, webhook, twitchChannel string
	serverListen, websocketPath, serverURL                                      string
	timestamps, noColor                                                         bool
}

func flags(name string, args []string) (opts, config.Config, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.config, "config", "", "JSON config file")
	fs.StringVar(&o.log, "log-file", "", "append normalized JSONL")
	fs.StringVar(&o.video, "youtube-video", "", "YouTube live URL or video ID")
	fs.StringVar(&o.key, "youtube-api-key", "", "YouTube API key (prefer setup or environment)")
	fs.StringVar(&o.token, "youtube-access-token", "", "YouTube OAuth access token")
	fs.StringVar(&o.broadcaster, "kick-broadcaster-id", "", "Kick broadcaster user ID")
	fs.StringVar(&o.listen, "kick-listen", "", "Kick loopback listen address")
	fs.StringVar(&o.webhook, "kick-webhook-url", "", "public Kick HTTPS webhook URL")
	fs.StringVar(&o.twitchChannel, "twitch-channel", "", "Twitch channel name or URL")
	fs.StringVar(&o.serverListen, "server-listen", "", "private server listen address")
	fs.StringVar(&o.websocketPath, "server-websocket-path", "", "server WebSocket endpoint path")
	fs.StringVar(&o.serverURL, "server-url", "", "remote Streamchat ws:// or wss:// endpoint")
	fs.BoolVar(&o.timestamps, "timestamps", false, "show timestamps")
	fs.BoolVar(&o.noColor, "no-color", false, "disable color")
	if e := fs.Parse(args); e != nil {
		return o, config.Config{}, e
	}
	c, e := config.Load(o.config)
	if e != nil {
		return o, c, e
	}
	config.ApplyEnv(&c, os.Getenv)
	if o.log != "" {
		c.LogFile = o.log
	}
	if o.video != "" {
		if e = mustSetTarget(&c, "youtube", o.video); e != nil {
			return o, c, e
		}
	}
	if o.key != "" {
		c.YouTube.APIKey = o.key
	}
	if o.token != "" {
		c.YouTube.AccessToken = o.token
	}
	if o.broadcaster != "" {
		c.Kick.BroadcasterID = o.broadcaster
	}
	if o.listen != "" {
		c.Kick.Listen = o.listen
	}
	if o.webhook != "" {
		c.Kick.WebhookURL = o.webhook
	}
	if o.twitchChannel != "" {
		if e = mustSetTarget(&c, "twitch", o.twitchChannel); e != nil {
			return o, c, e
		}
	}
	if o.serverListen != "" {
		c.Server.Listen = o.serverListen
	}
	if o.websocketPath != "" {
		c.Server.WebSocketPath = o.websocketPath
	}
	if o.serverURL != "" {
		c.Client.ServerURL = o.serverURL
	}
	if o.timestamps {
		c.Timestamps = true
	}
	if o.noColor {
		c.NoColor = true
	}
	return o, c, nil
}

func mustSetTarget(c *config.Config, name, value string) error {
	d, ok := platformreg.Default().Find(name)
	if !ok {
		return errors.New("unknown platform")
	}
	return d.SetTarget(c, value)
}

func runPlatforms(ctx context.Context, mode string, args []string, in io.Reader, out, errw io.Writer) error {
	o, c, e := flags(mode, args)
	if e != nil {
		return e
	}
	validationMode := "check"
	if mode == "run" {
		validationMode = "run"
	}
	if e = c.Validate(validationMode); e != nil {
		return e
	}
	if mode == "run" && c.Twitch.ClientID != "" && c.Twitch.AccessToken != "" {
		if e = prepareTwitch(ctx, &c, o.config); e != nil {
			return e
		}
	}
	r := platformreg.Default()
	explicit := map[string]bool{}
	if mode != "run" {
		explicit[mode] = true
	}
	reader := bufio.NewReader(in)
	for _, d := range r.Definitions() {
		if len(explicit) > 0 && !explicit[d.Name] {
			continue
		}
		if !d.Configured(c) {
			if explicit[d.Name] {
				return errors.New(d.DisplayName + " is not configured.\nRun:\n    streamchat setup " + d.Name)
			}
			continue
		}
		if mode == "run" && c.Client.ServerURL != "" && (d.Name == "kick" || d.Name == "youtube") {
			continue
		}
		if d.TargetPrompt != "" && d.Target(c) == "" {
			fmt.Fprintf(out, "%s: ", d.TargetPrompt)
			v, er := reader.ReadString('\n')
			if er != nil && strings.TrimSpace(v) == "" {
				return fmt.Errorf("%s is required; rerun with the corresponding flag", d.TargetPrompt)
			}
			if er = d.SetTarget(&c, strings.TrimSpace(v)); er != nil {
				return er
			}
		}
	}
	adapters, e := r.Select(&c, explicit)
	if e != nil {
		return e
	}
	if mode == "run" && c.Client.ServerURL != "" {
		adapters = useRemoteServer(adapters, c)
	}
	if len(adapters) == 0 {
		return errors.New("no runnable chat target is configured. Run 'streamchat setup', configure client.server_url, or supply --youtube-video/--twitch-channel")
	}
	var input io.Reader
	var targets *outbound.Session
	var status statusProvider
	if mode == "run" {
		input = reader
		if inputFile, ok := in.(*os.File); ok {
			input = &terminalInput{Reader: reader, file: inputFile}
		}
		kickClient := &kickOutboundSender{config: c.Kick, configPath: c.Path, http: &http.Client{Timeout: 20 * time.Second}}
		status = kickClient
		targets = outbound.NewTargets(outbound.Target{
			Name:        "kick",
			Aliases:     []string{"kick"},
			Sender:      kickClient,
			Unavailable: c.Kick.BroadcasterID == "" || (c.Kick.AccessToken == "" && c.Kick.RefreshToken == ""),
		})
		registerRetiredTargetCommands(targets)
		if state, stateErr := clientstate.Default(); stateErr == nil {
			configureOutboundState(targets, state)
		}
		targets.RegisterControl("title", outbound.ControlFunc(kickClient.Title))
		targets.RegisterControl("category", outbound.ControlFunc(kickClient.Category))
		moderation := newModerationControls(kickClient)
		targets.RegisterControl("ban", outbound.ControlFunc(moderation.Ban))
		targets.RegisterControl("timeout", outbound.ControlFunc(moderation.Timeout))
		remoteClear := newRemoteClearController(kickClient, archiveMessageIDFile{path: c.Storage.SQLitePath})
		targets.RegisterControl("clear", remoteClear)
		opener := openController{config: c, kick: kickClient, launcher: launcher.New()}
		targets.RegisterControl("open", outbound.ControlFunc(opener.Open))
		registerShutdownControls(targets)
	}
	return runAdapters(ctx, adapters, c, input, targets, status, out, errw)
}

func configureOutboundState(targets *outbound.Session, state *clientstate.Store) {
	if targets == nil || state == nil {
		return
	}
	saved := state.Load()
	targets.Restore(saved.LastOutboundTarget)
	targets.SetSelectionChanged(func(target string) {
		_ = state.Save(clientstate.State{LastOutboundTarget: target})
	})
}

func registerRetiredTargetCommands(targets *outbound.Session) {
	targets.RegisterControl("kk", outbound.ControlFunc(func(context.Context, string) (string, error) {
		return "", errors.New("unknown outbound target command; use /kick")
	}))
}

func useRemoteServer(adapters []chat.Adapter, c config.Config) []chat.Adapter {
	local := adapters[:0]
	for _, adapter := range adapters {
		if adapter.Name() != "kick" && adapter.Name() != "youtube" {
			local = append(local, adapter)
		}
	}
	return append(local, relay.NewClient(c.Client.ServerURL, c.RelayAuthToken))
}

func serve(ctx context.Context, args []string, out io.Writer) error {
	o, c, err := flags("serve", args)
	if err != nil {
		return err
	}
	if err = c.Validate("serve"); err != nil {
		return err
	}
	store, err := archivepkg.Open(c.Storage.SQLitePath)
	if err != nil {
		return err
	}
	defer store.Close()
	publicKey := []byte(kick.OfficialPublicKeyPEM)
	if c.Kick.PublicKeyPEM != "" {
		publicKey = []byte(c.Kick.PublicKeyPEM)
	}
	key, err := kick.ParsePublicKey(publicKey)
	if err != nil {
		return err
	}
	messages := make(chan chat.Message, c.QueueSize)
	webhook := kick.NewServer("", key, messages)
	webhook.MaxBody = c.Kick.MaxBodyBytes
	webhook.MaxAge = c.Kick.MaxAge
	server := relay.NewServer(c.Server.Listen, c.Server.WebSocketPath, c.RelayAuthToken, webhook.Handler())
	server.Accept = store.Store

	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(serverCtx, messages) }()

	youtubeEnabled := c.YouTube.RefreshToken != "" || (c.YouTube.VideoID != "" && (c.YouTube.APIKey != "" || c.YouTube.AccessToken != ""))
	var youtubeErr <-chan error
	if youtubeEnabled {
		yt := youtube.New(&http.Client{Timeout: 0}, c.YouTube.BaseURL, c.YouTube.APIKey, c.YouTube.AccessToken, c.YouTube.VideoID)
		yt.ClientID = c.YouTube.ClientID
		yt.ClientSecret = c.YouTube.ClientSecret
		yt.RefreshToken = c.YouTube.RefreshToken
		yt.TokenExpiry = c.YouTube.TokenExpiry
		yt.OnToken = func(tok youtube.Token) error {
			c.YouTube.AccessToken = tok.AccessToken
			c.YouTube.RefreshToken = tok.RefreshToken
			c.YouTube.TokenExpiry = yt.TokenExpiry
			if os.Getenv("STREAMCHAT_YOUTUBE_ACCESS_TOKEN") != "" || os.Getenv("STREAMCHAT_YOUTUBE_REFRESH_TOKEN") != "" {
				return nil
			}
			return persistYouTubeTokens(o.config, c.YouTube)
		}
		ch := make(chan error, 1)
		youtubeErr = ch
		go func() { ch <- yt.RunServer(serverCtx, messages) }()
	}
	fmt.Fprintf(out, "Streamchat server listening on %s (Kick webhook /webhooks/kick, relay %s)\n", c.Server.Listen, c.Server.WebSocketPath)
	fmt.Fprintf(out, "SQLite archive: %s\n", c.Storage.SQLitePath)
	if youtubeEnabled {
		fmt.Fprintln(out, "YouTube server ingestion enabled (active broadcast discovery + streamList).")
	}
	select {
	case <-ctx.Done():
		cancel()
		<-serverErr
		return nil
	case err = <-serverErr:
		return err
	case err = <-youtubeErr:
		cancel()
		<-serverErr
		if err == nil {
			return errors.New("YouTube server ingestion stopped unexpectedly")
		}
		return fmt.Errorf("YouTube server ingestion: %w", err)
	}
}

func archiveStats(ctx context.Context, args []string, out io.Writer) error {
	_, c, err := flags("archive stats", args)
	if err != nil {
		return err
	}
	if _, err = os.Stat(c.Storage.SQLitePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("SQLite archive does not exist at %s; start streamchat serve first", c.Storage.SQLitePath)
		}
		return fmt.Errorf("inspect SQLite archive %s: %w", c.Storage.SQLitePath, err)
	}
	store, err := archivepkg.Open(c.Storage.SQLitePath)
	if err != nil {
		return err
	}
	defer store.Close()
	stats, err := store.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "SQLite archive: %s\nSchema version: %d\nMessages: %d\n", c.Storage.SQLitePath, stats.SchemaVersion, stats.Total)
	for _, platform := range stats.Platforms {
		fmt.Fprintf(out, "  %s: %d\n", platform.Platform, platform.Count)
	}
	if !stats.First.IsZero() {
		fmt.Fprintf(out, "First event: %s\nLast event:  %s\n", stats.First.Format(time.RFC3339), stats.Last.Format(time.RFC3339))
	}
	return nil
}

func runAdapters(ctx context.Context, adapters []chat.Adapter, c config.Config, in io.Reader, targets *outbound.Session, status statusProvider, out, errw io.Writer) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var safeOut io.Writer = &lockedWriter{writer: out}
	var safeErr io.Writer = &lockedWriter{writer: errw}
	var terminal *terminalui.Terminal
	var emoteResolver emote.Resolver
	var inFile *os.File
	if terminalIn, ok := in.(*terminalInput); ok {
		inFile = terminalIn.file
	} else {
		inFile, _ = in.(*os.File)
	}
	if inFile != nil {
		if outFile, outputOK := out.(*os.File); outputOK && terminalui.IsInteractive(inFile, outFile) {
			var err error
			emotes := emote.NewDefaultControllerWithOptions(emote.DefaultControllerOptions{Mode: c.Emotes.Mode, TerminalOutput: outFile, Debug: c.Emotes.Debug})
			initialTarget := ""
			if targets != nil {
				initialTarget = targets.Selected()
			}
			terminal, err = terminalui.OpenWithBackendAndTarget(inFile, outFile, in, emotes, initialTarget)
			if err != nil {
				return fmt.Errorf("initialize interactive terminal: %w", err)
			}
			emotes.SetRedraw(terminal.Redraw)
			emoteResolver = emotes.Resolve
			safeOut = terminal.Writer(out)
			safeErr = terminal.Writer(errw)
			defer func() {
				if err := terminal.Close(); result == nil && err != nil {
					result = fmt.Errorf("restore terminal: %w", err)
				}
			}()
			if status != nil {
				statusCtx, stopStatus := context.WithCancel(ctx)
				trigger := make(chan struct{}, 1)
				done := make(chan struct{})
				if setter, ok := status.(interface{ setStatusRefresh(func()) }); ok {
					setter.setStatusRefresh(func() {
						select {
						case trigger <- struct{}{}:
						default:
						}
					})
				}
				go func() {
					defer close(done)
					runStatusRefresher(statusCtx, statusRefreshInterval, trigger, status, terminal)
				}()
				defer func() {
					stopStatus()
					<-done
					if setter, ok := status.(interface{ setStatusRefresh(func()) }); ok {
						setter.setStatusRefresh(nil)
					}
				}()
			}
		}
	}
	var shutdownRequests <-chan struct{}
	var inputDone <-chan struct{}
	if in != nil && targets != nil {
		clean := cleanController{}
		if terminal != nil {
			clean.display = terminal
		}
		targets.RegisterControl("clean", outbound.ControlFunc(clean.Clean))
		requests := make(chan struct{}, 1)
		done := make(chan struct{})
		shutdownRequests = requests
		inputDone = done
		go func() {
			defer close(done)
			requestShutdown := func() {
				select {
				case requests <- struct{}{}:
				default:
				}
			}
			if terminal != nil {
				runTerminalInput(ctx, terminal, targets, safeOut, safeErr, requestShutdown)
			} else {
				runOutboundInput(ctx, in, targets, safeOut, safeErr, requestShutdown)
			}
		}()
	}
	inputs := make([]<-chan chat.Message, 0, len(adapters))
	errc := make(chan error, len(adapters))
	for _, a := range adapters {
		ch := make(chan chat.Message, c.QueueSize)
		inputs = append(inputs, ch)
		go func(ad chat.Adapter, dst chan chat.Message) { defer close(dst); errc <- ad.Run(ctx, dst) }(a, ch)
	}
	ag, _ := aggregate.New(aggregate.Config{QueueSize: c.QueueSize, DuplicateCapacity: c.DuplicateCapacity, ReorderWindow: 100 * time.Millisecond})
	merged, aerrs := ag.Run(ctx, inputs...)
	colorAllocator := chattercolor.NewAllocator()
	renderOptions := render.Options{Timestamps: c.Timestamps, Color: render.ColorEnabled(c.NoColor), Emotes: emoteResolver, ChatColorMode: c.ChatColorMode, ChatterColorSource: colorAllocator}
	term := render.New(safeOut, renderOptions)
	formatter := render.New(io.Discard, renderOptions)
	var log *logging.Logger
	var e error
	if c.LogFile != "" {
		log, e = logging.Open(c.LogFile)
		if e != nil {
			return e
		}
		defer log.Close()
	}
	remaining := len(adapters)
	adapterErrs := (<-chan error)(errc)
	var firstAdapterError error
	for remaining > 0 || merged != nil {
		select {
		case <-shutdownRequests:
			cancel()
			<-inputDone
			for remaining > 0 {
				<-errc
				remaining--
			}
			return nil
		case <-ctx.Done():
			return nil
		case er := <-adapterErrs:
			remaining--
			if er != nil && !errors.Is(er, context.Canceled) {
				fmt.Fprintf(safeErr, "adapter error: %s\n", safeError(er))
				if firstAdapterError == nil {
					firstAdapterError = er
				}
			}
			if remaining == 0 {
				adapterErrs = nil
			}
		case er, ok := <-aerrs:
			if !ok {
				aerrs = nil
			} else if er != nil {
				fmt.Fprintf(safeErr, "aggregate error: %s\n", er)
			}
		case m, ok := <-merged:
			if !ok {
				merged = nil
				continue
			}
			if m.Platform == chat.PlatformKick {
				m.Emotes = kick.EnrichEmotes(m.Text, m.Emotes)
			}
			if terminal == nil {
				e = term.Render(m)
			} else {
				formatted := formatter.Format(m)
				author := render.Sanitize(m.AuthorDisplayName)
				if author == "" {
					author = "system"
				}
				message := m
				e = terminal.AppendMessage(terminalui.DisplayMessage{ID: m.ID, Platform: string(m.Platform), Author: author, Line: formatted.Text, Render: func() emote.Line { return formatter.Format(message) }})
			}
			if e != nil {
				return e
			}
			if log != nil {
				if e = log.Write(m); e != nil {
					return fmt.Errorf("JSONL logging failed; terminating to avoid silent data loss: %w", e)
				}
			}
		}
	}
	return firstAdapterError
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

type terminalInput struct {
	io.Reader
	file *os.File
}

type statusProvider interface {
	StreamStatus(context.Context) (terminalui.StreamStatus, error)
}

type statusDisplay interface {
	SetStatus(terminalui.StreamStatus)
}

func runStatusRefresher(ctx context.Context, interval time.Duration, trigger <-chan struct{}, provider statusProvider, display statusDisplay) {
	refresh := func() {
		status, err := provider.StreamStatus(ctx)
		if err == nil {
			display.SetStatus(status)
		}
	}
	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		case <-trigger:
			refresh()
		}
	}
}

type localDisplay interface {
	CleanAll() (int, error)
	CleanPlatform(string) (int, error)
	CleanAuthor(string) (int, error)
}

type cleanController struct {
	display localDisplay
}

func (c cleanController) Clean(_ context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 1 {
		return "Usage: /clean streamchat|kick|USER", nil
	}
	if c.display == nil {
		return "Local display cleaning requires an interactive terminal.", nil
	}
	target := fields[0]
	var removed int
	var err error
	switch strings.ToLower(target) {
	case "streamchat":
		removed, err = c.display.CleanAll()
	case "kick":
		removed, err = c.display.CleanPlatform("kick")
	case "youtube", "twitch":
		return "Display cleaning is not implemented for: " + target, nil
	default:
		removed, err = c.display.CleanAuthor(target)
	}
	if err != nil {
		return "", fmt.Errorf("redraw local display: %w", err)
	}
	if removed == 0 {
		return "No displayed messages matched: " + target, nil
	}
	return "", nil
}

type archiveMessageIDSource interface {
	MessageIDsSince(context.Context, chat.Platform, time.Time) ([]string, error)
}

type archiveMessageIDFile struct{ path string }

func (s archiveMessageIDFile) MessageIDsSince(ctx context.Context, platform chat.Platform, since time.Time) ([]string, error) {
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("SQLite archive does not exist at %s; start streamchat serve or configure storage.sqlite_path", s.path)
		}
		return nil, fmt.Errorf("inspect SQLite archive %s: %w", s.path, err)
	}
	store, err := archivepkg.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.MessageIDsSince(ctx, platform, since)
}

type remoteClearResult struct {
	Deleted int
	Failed  int
}

type remoteClearPlatform interface {
	ClearMessages(context.Context, []string) (remoteClearResult, error)
}

type remoteClearController struct {
	source    archiveMessageIDSource
	platforms map[string]remoteClearPlatform
	now       func() time.Time
}

func newRemoteClearController(kickClient *kickOutboundSender, source archiveMessageIDSource) *remoteClearController {
	return &remoteClearController{source: source, platforms: map[string]remoteClearPlatform{"kick": kickClient}, now: time.Now}
}

func (c *remoteClearController) Execute(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) < 1 || len(fields) > 2 {
		return "Usage: /clear kick [Nd] (example: /clear kick 3d)", nil
	}
	platform := strings.ToLower(fields[0])
	provider, ok := c.platforms[platform]
	if !ok {
		if platform == "youtube" || platform == "twitch" {
			return "Remote chat clearing is not implemented for: " + platform, nil
		}
		return "Unsupported clear platform: " + fields[0] + ". Supported: kick.", nil
	}
	if c.source == nil {
		return "SQLite archive is unavailable for remote clearing.", nil
	}
	days := 1
	if len(fields) == 2 {
		var err error
		days, err = parseClearDays(fields[1])
		if err != nil {
			return err.Error(), nil
		}
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	since := now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	ids, err := c.source.MessageIDsSince(ctx, chat.PlatformKick, since)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return fmt.Sprintf("No archived Kick messages to clear in the last %dd.", days), nil
	}
	result, err := provider.ClearMessages(ctx, ids)
	if err != nil {
		return "", err
	}
	if result.Failed > 0 {
		return fmt.Sprintf("Cleared Kick chat: %d deleted, %d failed", result.Deleted, result.Failed), nil
	}
	noun := "messages"
	if result.Deleted == 1 {
		noun = "message"
	}
	return fmt.Sprintf("Cleared Kick chat: %d %s", result.Deleted, noun), nil
}

func parseClearDays(value string) (int, error) {
	const maxDays = 3650
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 || value[len(value)-1] != 'd' {
		return 0, errors.New("clear window must be a positive number of days up to 3650 (example: 3d)")
	}
	digits := value[:len(value)-1]
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, errors.New("clear window must be a positive number of days up to 3650 (example: 3d)")
		}
	}
	days, err := strconv.Atoi(digits)
	if err != nil || days < 1 || days > maxDays {
		return 0, errors.New("clear window must be a positive number of days up to 3650 (example: 3d)")
	}
	return days, nil
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func runOutboundInput(ctx context.Context, in io.Reader, targets *outbound.Session, out, errw io.Writer, requestShutdown func()) {
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		if processInputLine(ctx, scanner.Text(), targets, out, errw) {
			requestShutdown()
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		fmt.Fprintf(errw, "terminal input failed: %s\n", safeError(err))
	}
}

func runTerminalInput(ctx context.Context, terminal *terminalui.Terminal, targets *outbound.Session, out, errw io.Writer, requestShutdown func()) {
	for {
		event, err := terminal.Next()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				fmt.Fprintf(errw, "terminal input failed: %s\n", safeError(err))
			}
			requestShutdown()
			return
		}
		if event.Shutdown {
			requestShutdown()
			return
		}
		if !event.Submit {
			continue
		}
		shutdown := processInputLine(ctx, event.Line, targets, out, errw)
		terminal.SetTarget(targets.Selected())
		if shutdown {
			requestShutdown()
			return
		}
	}
}

func processInputLine(ctx context.Context, line string, targets *outbound.Session, out, errw io.Writer) bool {
	result, err := targets.Process(ctx, line)
	if err != nil {
		if errors.Is(err, outbound.ErrShutdownRequested) {
			return true
		}
		if errors.Is(err, outbound.ErrNoTarget) {
			fmt.Fprintln(errw, outbound.NoTargetInstruction)
		} else if !errors.Is(err, context.Canceled) {
			var controlErr *outbound.ControlError
			if errors.As(err, &controlErr) {
				fmt.Fprintf(errw, "control failed: %s\n", sanitizeLocalOutput(safeError(err)))
			} else {
				fmt.Fprintf(errw, "send failed: %s\n", safeError(err))
			}
		}
	} else if result != "" {
		fmt.Fprintln(out, sanitizeLocalOutput(result))
	}
	return false
}

func registerShutdownControls(targets *outbound.Session) {
	shutdown := outbound.ControlFunc(func(context.Context, string) (string, error) {
		return "", outbound.ErrShutdownRequested
	})
	targets.RegisterControl("exit", shutdown)
	targets.RegisterControl("quit", shutdown)
}

func sanitizeLocalOutput(value string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = render.Sanitize(lines[i])
	}
	return strings.Join(lines, "\n")
}

type kickOutboundSender struct {
	mu         sync.Mutex
	refreshMu  sync.RWMutex
	refreshNow func()
	config     config.Kick
	configPath string
	http       *http.Client
}

func (s *kickOutboundSender) Send(ctx context.Context, message string) error {
	return s.withToken(ctx, func(accessToken string) error {
		client := kick.ChatClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		return client.Send(ctx, s.config.BroadcasterID, message)
	})
}

func (s *kickOutboundSender) Title(ctx context.Context, title string) (string, error) {
	if title == "" {
		return "Usage: /title NEW STREAM TITLE", nil
	}
	err := s.withToken(ctx, func(accessToken string) error {
		client := kick.ChannelClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		return client.UpdateTitle(ctx, title)
	})
	if err != nil {
		return "", err
	}
	s.requestStatusRefresh()
	return "Title updated: " + title, nil
}

func (s *kickOutboundSender) Category(ctx context.Context, argument string) (string, error) {
	if argument == "" {
		return "Usage: /category CATEGORY NAME OR ID", nil
	}
	var category kick.Category
	err := s.withToken(ctx, func(accessToken string) error {
		client := kick.ChannelClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		resolved, err := client.ResolveCategory(ctx, argument)
		if err != nil {
			return err
		}
		if err = client.UpdateCategory(ctx, resolved.ID); err != nil {
			return err
		}
		category = resolved
		return nil
	})
	if err != nil {
		return "", err
	}
	s.requestStatusRefresh()
	return "Category updated: " + category.Name, nil
}

func (s *kickOutboundSender) StreamStatus(ctx context.Context) (terminalui.StreamStatus, error) {
	var status kick.ChannelStatus
	err := s.withToken(ctx, func(accessToken string) error {
		client := kick.ChannelClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		var err error
		status, err = client.GetStatus(ctx)
		return err
	})
	return terminalui.StreamStatus{Title: status.Title, Category: status.Category, ViewerCount: status.ViewerCount, Live: status.Live}, err
}

func (s *kickOutboundSender) setStatusRefresh(refresh func()) {
	s.refreshMu.Lock()
	s.refreshNow = refresh
	s.refreshMu.Unlock()
}

func (s *kickOutboundSender) requestStatusRefresh() {
	s.refreshMu.RLock()
	refresh := s.refreshNow
	s.refreshMu.RUnlock()
	if refresh != nil {
		refresh()
	}
}

type moderationPlatform interface {
	BanUser(context.Context, string) (string, error)
	TimeoutUser(context.Context, string, string) (string, error)
}

type moderationControls struct {
	platforms map[string]moderationPlatform
}

func newModerationControls(kickClient *kickOutboundSender) moderationControls {
	return moderationControls{platforms: map[string]moderationPlatform{"kick": kickClient}}
}

func (m moderationControls) Ban(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 2 {
		return "Usage: /ban PLATFORM USER (supported: kick)", nil
	}
	provider, ok := m.platforms[strings.ToLower(fields[0])]
	if !ok {
		return "Unsupported moderation platform: " + fields[0] + ". Supported: kick.", nil
	}
	return provider.BanUser(ctx, fields[1])
}

func (m moderationControls) Timeout(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 3 {
		return "Usage: /timeout PLATFORM USER DURATION (example: /timeout kick USER 10m)", nil
	}
	provider, ok := m.platforms[strings.ToLower(fields[0])]
	if !ok {
		return "Unsupported moderation platform: " + fields[0] + ". Supported: kick.", nil
	}
	return provider.TimeoutUser(ctx, fields[1], fields[2])
}

func (s *kickOutboundSender) BanUser(ctx context.Context, username string) (string, error) {
	var user kick.ModerationUser
	err := s.withToken(ctx, func(accessToken string) error {
		client := kick.ModerationClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		var err error
		user, err = client.Ban(ctx, s.config.BroadcasterID, username)
		return err
	})
	if err != nil {
		return "", err
	}
	return "Banned: " + user.Username, nil
}

func (s *kickOutboundSender) TimeoutUser(ctx context.Context, username, duration string) (string, error) {
	minutes, err := kick.ParseTimeoutDuration(duration)
	if err != nil {
		return "", err
	}
	var user kick.ModerationUser
	err = s.withToken(ctx, func(accessToken string) error {
		client := kick.ModerationClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		user, err = client.Timeout(ctx, s.config.BroadcasterID, username, minutes)
		return err
	})
	if err != nil {
		return "", err
	}
	return "Timed out: " + user.Username + " for " + strings.ToLower(duration), nil
}

func (s *kickOutboundSender) ClearMessages(ctx context.Context, messageIDs []string) (remoteClearResult, error) {
	var result remoteClearResult
	err := s.withToken(ctx, func(accessToken string) error {
		result = remoteClearResult{}
		client := kick.ChatDeleteClient{HTTP: s.http, BaseURL: s.config.APIBaseURL, AccessToken: accessToken}
		for index, messageID := range messageIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := client.DeleteMessage(ctx, messageID)
			switch {
			case err == nil, errors.Is(err, kick.ErrChatDeleteNotFound):
				result.Deleted++
			case errors.Is(err, kick.ErrChatDeleteAuthentication), errors.Is(err, kick.ErrChatDeleteScope):
				return err
			case errors.Is(err, kick.ErrChatDeleteRateLimit):
				// Stop the batch instead of hammering a rate-limited endpoint. The
				// current and remaining IDs are reported as not deleted.
				result.Failed += len(messageIDs) - index
				return nil
			default:
				result.Failed++
			}
		}
		return nil
	})
	return result, err
}

func (s *kickOutboundSender) withToken(ctx context.Context, operation func(string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	refreshed := false
	if s.config.AccessToken == "" || (!s.config.TokenExpiry.IsZero() && time.Until(s.config.TokenExpiry) < time.Minute) {
		if s.config.RefreshToken == "" {
			return errors.New("Kick authorization is missing or expired; run: streamchat setup kick")
		}
		if err := s.refresh(ctx); err != nil {
			return err
		}
		refreshed = true
	}
	err := operation(s.config.AccessToken)
	if !refreshed && kickAuthenticationError(err) && s.config.RefreshToken != "" {
		if refreshErr := s.refresh(ctx); refreshErr != nil {
			return refreshErr
		}
		return operation(s.config.AccessToken)
	}
	return err
}

func kickAuthenticationError(err error) bool {
	return errors.Is(err, kick.ErrChatAuthentication) || errors.Is(err, kick.ErrChannelAuthentication) || errors.Is(err, kick.ErrModerationAuthentication) || errors.Is(err, kick.ErrChatDeleteAuthentication)
}

func (s *kickOutboundSender) refresh(ctx context.Context) error {
	client := kick.OAuthClient{HTTP: s.http, OAuthBaseURL: s.config.OAuthBaseURL, APIBaseURL: s.config.APIBaseURL, ClientID: s.config.ClientID, ClientSecret: s.config.ClientSecret}
	tok, err := client.Refresh(ctx, s.config.RefreshToken)
	if err != nil {
		return err
	}
	s.config.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		s.config.RefreshToken = tok.RefreshToken
	}
	s.config.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if os.Getenv("STREAMCHAT_KICK_ACCESS_TOKEN") != "" || os.Getenv("STREAMCHAT_KICK_REFRESH_TOKEN") != "" {
		return nil
	}
	return persistKickTokens(s.configPath, s.config)
}

func demo(ctx context.Context, args []string, out io.Writer) error {
	o, c, e := flags("demo", args)
	if e != nil {
		return e
	}
	base := time.Date(2026, 1, 2, 12, 41, 3, 0, time.UTC)
	a := make(chan chat.Message, 8)
	b := make(chan chat.Message, 8)
	msgs := []chat.Message{{ID: "yt-1", Platform: chat.PlatformYouTube, Timestamp: base, AuthorDisplayName: "alice", Badges: []chat.Badge{{Type: "moderator", Text: "MOD"}}, Roles: chat.NewRoleSet(chat.RoleModerator), Text: "Good morning!", EventType: chat.EventMessage}, {ID: "k-1", Platform: chat.PlatformKick, Timestamp: base.Add(2 * time.Second), AuthorDisplayName: "bob", Badges: []chat.Badge{{Type: "subscriber", Text: "SUB"}}, Roles: chat.NewRoleSet(chat.RoleSubscriber), Text: "First time watching here", Reply: &chat.Reply{MessageID: "k-0", AuthorDisplayName: "carol"}, EventType: chat.EventMessage}, {ID: "tw-1", Platform: chat.PlatformTwitch, Timestamp: base.Add(3 * time.Second), AuthorDisplayName: "eve", Text: "Hello from Twitch", EventType: chat.EventMessage}, {ID: "yt-2", Platform: chat.PlatformYouTube, Timestamp: base.Add(4 * time.Second), AuthorDisplayName: "dana", Text: "Welcome!", EventType: chat.EventPaid, Paid: &chat.Paid{Display: "$5.00"}}, {ID: "yt-2", Platform: chat.PlatformYouTube, Timestamp: base.Add(4 * time.Second), Text: "duplicate", EventType: chat.EventPaid}}
	for _, m := range []chat.Message{msgs[3], msgs[0], msgs[4]} {
		a <- m
	}
	for _, m := range []chat.Message{msgs[1], msgs[2]} {
		b <- m
	}
	close(a)
	close(b)
	ag, _ := aggregate.New(aggregate.Config{QueueSize: 8, DuplicateCapacity: 16, ReorderWindow: time.Millisecond})
	merged, _ := ag.Run(ctx, a, b)
	term := render.New(out, render.Options{Timestamps: o.timestamps, Color: render.ColorEnabled(o.noColor), ChatColorMode: c.ChatColorMode, ChatterColorSource: chattercolor.NewAllocator()})
	for m := range merged {
		if e := term.Render(m); e != nil {
			return e
		}
	}
	return nil
}

func showConfig(args []string, out io.Writer) error {
	_, c, e := flags("config show", args)
	if e != nil {
		return e
	}
	b, e := config.RedactedJSON(c)
	if e != nil {
		return e
	}
	_, e = fmt.Fprintln(out, string(b))
	return e
}

func check(args []string, out io.Writer) error {
	o, c, e := flags("config check", args)
	if e != nil {
		return e
	}
	if e = c.Validate("check"); e != nil {
		return e
	}
	if e = config.CheckFileMode(o.config); e != nil {
		return e
	}
	fmt.Fprintln(out, "configuration valid (file and structural settings)")
	for _, d := range platformreg.Default().Definitions() {
		status := "not configured — run: streamchat setup " + d.Name
		if d.Configured(c) {
			status = "configured"
			if d.TargetPrompt != "" && d.Target(c) == "" {
				status = "credentials configured; runtime target will be requested"
			}
		}
		fmt.Fprintf(out, "%-8s %s\n", d.DisplayName+":", status)
	}
	if c.Kick.WebhookURL != "" {
		fmt.Fprintf(out, "Kick URL: %s (must match the webhook URL in the Kick developer portal)\n", c.Kick.WebhookURL)
		fmt.Fprintln(out, "          This local value is not sent to Kick; changing it alone does not change the destination.")
		fmt.Fprintln(out, "          After changing the portal URL, run: streamchat kick subscribe")
	}
	youtubeServerStatus := "not configured — run: streamchat setup youtube-server"
	if c.YouTube.ClientID != "" && c.YouTube.ClientSecret != "" && c.YouTube.RefreshToken != "" {
		youtubeServerStatus = "configured for active-broadcast discovery and streamList"
	}
	fmt.Fprintf(out, "%-16s %s\n", "YouTube server:", youtubeServerStatus)
	fmt.Fprintf(out, "%-16s %s\n", "SQLite archive:", c.Storage.SQLitePath)
	relayStatus := "not configured"
	if c.Client.ServerURL != "" && c.RelayAuthToken != "" {
		relayStatus = "configured for " + c.Client.ServerURL
	}
	fmt.Fprintf(out, "%-8s %s\n", "Server:", relayStatus)
	return nil
}

func subscribe(ctx context.Context, args []string, out io.Writer) error {
	inspect := false
	rest := []string{}
	for _, a := range args {
		if a == "--inspect" {
			inspect = true
		} else {
			rest = append(rest, a)
		}
	}
	o, c, e := flags("subscribe", rest)
	if e != nil {
		return e
	}
	if c.Kick.AccessToken == "" {
		return errors.New("Kick is not authorized. Run: streamchat setup kick (it requests events:subscribe and stores a user access token)")
	}
	if c.Kick.BroadcasterID == "" {
		return errors.New("Kick broadcaster user ID is missing. Run: streamchat setup kick so Streamchat resolves it from the authorized account")
	}
	if !c.Kick.TokenExpiry.IsZero() && time.Until(c.Kick.TokenExpiry) < time.Minute && c.Kick.RefreshToken != "" {
		oc := kick.OAuthClient{HTTP: &http.Client{Timeout: 20 * time.Second}, OAuthBaseURL: c.Kick.OAuthBaseURL, APIBaseURL: c.Kick.APIBaseURL, ClientID: c.Kick.ClientID, ClientSecret: c.Kick.ClientSecret}
		tok, err := oc.Refresh(ctx, c.Kick.RefreshToken)
		if err != nil {
			return err
		}
		c.Kick.AccessToken = tok.AccessToken
		c.Kick.RefreshToken = tok.RefreshToken
		c.Kick.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		if os.Getenv("STREAMCHAT_KICK_ACCESS_TOKEN") == "" {
			if err = persistKickTokens(o.config, c.Kick); err != nil {
				return err
			}
		}
	}
	method := http.MethodPost
	if inspect {
		method = http.MethodGet
	}
	cl := kick.SubscriptionClient{HTTP: &http.Client{Timeout: 20 * time.Second}, BaseURL: c.Kick.APIBaseURL, AccessToken: c.Kick.AccessToken}
	b, e := cl.Do(ctx, method, c.Kick.BroadcasterID)
	if e != nil {
		return e
	}
	var compact any
	if json.Unmarshal(b, &compact) == nil {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(compact)
	}
	_, e = fmt.Fprintln(out, string(b))
	return e
}

func prepareTwitch(ctx context.Context, c *config.Config, path string) error {
	api := &twitch.API{HTTP: &http.Client{Timeout: 20 * time.Second}, APIBaseURL: c.Twitch.APIBaseURL, OAuthBaseURL: c.Twitch.OAuthBaseURL, ClientID: c.Twitch.ClientID, ClientSecret: c.Twitch.ClientSecret, AccessToken: c.Twitch.AccessToken}
	refresh := !c.Twitch.TokenExpiry.IsZero() && time.Until(c.Twitch.TokenExpiry) < time.Minute
	if !refresh {
		if identity, err := api.ValidateToken(ctx); err != nil {
			var ae *chat.AdapterError
			if errors.As(err, &ae) && ae.Kind == chat.Authentication {
				refresh = true
			} else {
				return err
			}
		} else {
			c.Twitch.UserID = identity.UserID
			c.Twitch.UserLogin = identity.Login
		}
	}
	if !refresh {
		return nil
	}
	if c.Twitch.RefreshToken == "" {
		return errors.New("Twitch authorization expired and no refresh token is stored. Run: streamchat setup twitch")
	}
	tok, err := api.Refresh(ctx, c.Twitch.RefreshToken)
	if err != nil {
		return err
	}
	c.Twitch.AccessToken = tok.AccessToken
	c.Twitch.RefreshToken = tok.RefreshToken
	c.Twitch.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	api.AccessToken = tok.AccessToken
	identity, err := api.ValidateToken(ctx)
	if err != nil {
		return err
	}
	c.Twitch.UserID = identity.UserID
	c.Twitch.UserLogin = identity.Login
	if os.Getenv("STREAMCHAT_TWITCH_ACCESS_TOKEN") == "" {
		return persistTwitchTokens(path, c.Twitch)
	}
	return nil
}

func persistTwitchTokens(path string, v config.Twitch) error {
	c, e := config.Load(path)
	if e != nil {
		return e
	}
	c.Twitch.AccessToken = v.AccessToken
	c.Twitch.RefreshToken = v.RefreshToken
	c.Twitch.TokenExpiry = v.TokenExpiry
	c.Twitch.UserID = v.UserID
	c.Twitch.UserLogin = v.UserLogin
	return config.Save(path, c)
}
func persistKickTokens(path string, v config.Kick) error {
	c, e := config.Load(path)
	if e != nil {
		return e
	}
	c.Kick.AccessToken = v.AccessToken
	c.Kick.RefreshToken = v.RefreshToken
	c.Kick.TokenExpiry = v.TokenExpiry
	return config.Save(path, c)
}

func persistYouTubeTokens(path string, v config.YouTube) error {
	c, e := config.Load(path)
	if e != nil {
		return e
	}
	c.YouTube.AccessToken = v.AccessToken
	c.YouTube.RefreshToken = v.RefreshToken
	c.YouTube.TokenExpiry = v.TokenExpiry
	return config.Save(path, c)
}

func safeError(e error) string {
	s := e.Error()
	for _, marker := range []string{"access_token=", "refresh_token=", "client_secret=", "relay_auth_token=", "Authorization: Bearer ", "Authorization: OAuth "} {
		start := 0
		for start < len(s) {
			low := strings.ToLower(s[start:])
			rel := strings.Index(low, strings.ToLower(marker))
			if rel < 0 {
				break
			}
			i := start + rel
			end := strings.IndexAny(s[i+len(marker):], "& \n\r\t")
			if end < 0 {
				end = len(s) - i - len(marker)
			}
			s = s[:i+len(marker)] + "<redacted>" + s[i+len(marker)+end:]
			start = i + len(marker) + len("<redacted>")
		}
	}
	return s
}
