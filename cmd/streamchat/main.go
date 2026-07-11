package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/SleepyMario/streamchat/internal/aggregate"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/logging"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
	"github.com/SleepyMario/streamchat/internal/render"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"
const usage = `Streamchat merges official YouTube and Kick live-chat events.

Usage:
  streamchat --help
  streamchat version
  streamchat demo [--no-color] [--timestamps]
  streamchat run [platform and output flags]
  streamchat youtube --youtube-video ID [--youtube-api-key KEY]
  streamchat kick serve [--kick-listen 127.0.0.1:8788]
  streamchat kick subscribe [--inspect] --kick-broadcaster-id ID
  streamchat config check [--config FILE]

Common flags: --config, --log-file, --timestamps, --no-color
Run flags: --youtube-video, --youtube-api-key, --kick-broadcaster-id,
           --kick-listen, --kick-webhook-url
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errw io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, usage)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var e error
	switch args[0] {
	case "version":
		fmt.Fprintf(out, "streamchat %s\n", version)
		return 0
	case "demo":
		e = demo(ctx, args[1:], out)
	case "youtube":
		e = platform(ctx, "youtube", args[1:], out, errw)
	case "run":
		e = platform(ctx, "run", args[1:], out, errw)
	case "kick":
		if len(args) < 2 {
			e = errors.New("kick requires serve or subscribe")
		} else if args[1] == "serve" {
			e = platform(ctx, "kick", args[2:], out, errw)
		} else if args[1] == "subscribe" {
			e = subscribe(ctx, args[2:], out)
		} else {
			e = errors.New("unknown kick command")
		}
	case "config":
		if len(args) == 2 && args[1] == "check" {
			e = check(args[2:], out)
		} else {
			e = errors.New("config requires check")
		}
	default:
		e = errors.New("unknown command")
	}
	if e != nil {
		fmt.Fprintf(errw, "streamchat: %s\n", safeError(e))
		var ae *chat.AdapterError
		if errors.As(e, &ae) && ae.Kind == chat.Authentication {
			return 4
		}
		return 2
	}
	return 0
}

type opts struct {
	config, log, video, key, token, broadcaster, listen, webhook string
	timestamps, noColor                                          bool
}

func flags(name string, args []string) (opts, config.Config, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.config, "config", "", "JSON config file")
	fs.StringVar(&o.log, "log-file", "", "append normalized JSONL")
	fs.StringVar(&o.video, "youtube-video", "", "YouTube video/broadcast ID")
	fs.StringVar(&o.key, "youtube-api-key", "", "YouTube API key")
	fs.StringVar(&o.token, "youtube-access-token", "", "YouTube OAuth access token")
	fs.StringVar(&o.broadcaster, "kick-broadcaster-id", "", "Kick broadcaster user ID")
	fs.StringVar(&o.listen, "kick-listen", "", "Kick listen address")
	fs.StringVar(&o.webhook, "kick-webhook-url", "", "public Kick webhook URL (documentation/diagnostics)")
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
		c.YouTube.VideoID = o.video
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
	if o.timestamps {
		c.Timestamps = true
	}
	if o.noColor {
		c.NoColor = true
	}
	return o, c, nil
}
func platform(ctx context.Context, mode string, args []string, out, errw io.Writer) error {
	_, c, e := flags(mode, args)
	if e != nil {
		return e
	}
	ytOn := mode == "youtube" || mode == "run" && c.YouTube.VideoID != ""
	kickOn := mode == "kick" || mode == "run" && c.Kick.BroadcasterID != ""
	if !ytOn && !kickOn {
		return errors.New("configure at least one platform")
	}
	if ytOn {
		if e = c.Validate("youtube"); e != nil {
			return e
		}
	}
	if kickOn {
		if e = c.Validate("kick"); e != nil {
			return e
		}
	}
	key, _ := kick.ParsePublicKey([]byte(kick.OfficialPublicKeyPEM))
	var adapters []chat.Adapter
	var writable []chan chat.Message
	var inputs []<-chan chat.Message
	if ytOn {
		ch := make(chan chat.Message, c.QueueSize)
		writable = append(writable, ch)
		inputs = append(inputs, ch)
		adapters = append(adapters, youtube.New(nil, c.YouTube.BaseURL, c.YouTube.APIKey, c.YouTube.AccessToken, c.YouTube.VideoID))
	}
	if kickOn {
		ch := make(chan chat.Message, c.QueueSize)
		writable = append(writable, ch)
		inputs = append(inputs, ch)
		adapters = append(adapters, kick.NewServer(c.Kick.Listen, key, ch))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, len(adapters))
	for i, a := range adapters {
		go func(ad chat.Adapter, dst chan chat.Message) { errc <- ad.Run(ctx, dst); close(dst) }(a, writable[i])
	}
	ag, _ := aggregate.New(aggregate.Config{QueueSize: c.QueueSize, DuplicateCapacity: c.DuplicateCapacity, ReorderWindow: 100 * time.Millisecond})
	merged, aerrs := ag.Run(ctx, inputs...)
	term := render.New(out, render.Options{Timestamps: c.Timestamps, Color: render.ColorEnabled(c.NoColor)})
	var log *logging.Logger
	if c.LogFile != "" {
		log, e = logging.Open(c.LogFile)
		if e != nil {
			return e
		}
		defer log.Close()
	}
	remaining := len(adapters)
	for remaining > 0 || merged != nil {
		select {
		case <-ctx.Done():
			return nil
		case e := <-errc:
			remaining--
			if e != nil && !errors.Is(e, context.Canceled) {
				fmt.Fprintf(errw, "adapter error: %s\n", safeError(e))
			}
			if remaining == 0 {
				cancel()
			}
		case e, ok := <-aerrs:
			if ok && e != nil {
				fmt.Fprintf(errw, "aggregate error: %s\n", e)
			}
		case m, ok := <-merged:
			if !ok {
				merged = nil
				continue
			}
			if e = term.Render(m); e != nil {
				return e
			}
			if log != nil {
				if e = log.Write(m); e != nil {
					return fmt.Errorf("JSONL logging failed; terminating to avoid silent data loss: %w", e)
				}
			}
		}
	}
	return nil
}
func demo(ctx context.Context, args []string, out io.Writer) error {
	o, _, e := flags("demo", args)
	if e != nil {
		return e
	}
	base := time.Date(2026, 1, 2, 12, 41, 3, 0, time.UTC)
	a := make(chan chat.Message, 8)
	b := make(chan chat.Message, 8)
	msgs := []chat.Message{{ID: "yt-1", Platform: chat.PlatformYouTube, Timestamp: base, AuthorDisplayName: "alice", Badges: []chat.Badge{{Type: "moderator", Text: "MOD"}}, Text: "Good morning!", EventType: chat.EventMessage}, {ID: "k-1", Platform: chat.PlatformKick, Timestamp: base.Add(2 * time.Second), AuthorDisplayName: "bob", Badges: []chat.Badge{{Type: "subscriber", Text: "SUB"}}, Text: "First time watching here [emote:42:WAVE]", Emotes: []chat.Emote{{ID: "42", Name: "WAVE"}}, Reply: &chat.Reply{MessageID: "k-0", AuthorDisplayName: "carol"}, EventType: chat.EventMessage}, {ID: "yt-2", Platform: chat.PlatformYouTube, Timestamp: base.Add(4 * time.Second), AuthorDisplayName: "dana", Text: "Welcome!", EventType: chat.EventPaid, Paid: &chat.Paid{Display: "$5.00", Currency: "USD", AmountMicros: 5000000}}, {ID: "yt-2", Platform: chat.PlatformYouTube, Timestamp: base.Add(4 * time.Second), AuthorDisplayName: "dana", Text: "duplicate", EventType: chat.EventPaid}}
	a <- msgs[2]
	a <- msgs[0]
	a <- msgs[3]
	b <- msgs[1]
	close(a)
	close(b)
	ag, _ := aggregate.New(aggregate.Config{QueueSize: 8, DuplicateCapacity: 16, ReorderWindow: time.Millisecond})
	merged, _ := ag.Run(ctx, a, b)
	term := render.New(out, render.Options{Timestamps: o.timestamps, Color: render.ColorEnabled(o.noColor)})
	for m := range merged {
		if e := term.Render(m); e != nil {
			return e
		}
	}
	return nil
}
func check(args []string, out io.Writer) error {
	_, c, e := flags("check", args)
	if e != nil {
		return e
	}
	if e = c.Validate("check"); e != nil {
		return e
	}
	fmt.Fprintf(out, "configuration valid\nYouTube API key: %s\nKick access token: %s\nKick listen: %s\n", config.Redact(c.YouTube.APIKey), config.Redact(c.Kick.AccessToken), c.Kick.Listen)
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
	_, c, e := flags("subscribe", rest)
	if e != nil {
		return e
	}
	if c.Kick.AccessToken == "" {
		return errors.New("STREAMCHAT_KICK_ACCESS_TOKEN is required; token needs events:subscribe scope")
	}
	if c.Kick.BroadcasterID == "" {
		return errors.New("Kick broadcaster ID is required")
	}
	if e = c.Validate("check"); e != nil {
		return e
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
	_, e = fmt.Fprintln(out, string(b))
	return e
}
func safeError(e error) string {
	s := e.Error()
	for _, p := range []string{"key=", "access_token=", "Authorization: Bearer "} {
		if i := strings.Index(s, p); i >= 0 {
			s = s[:i] + p + "<redacted>"
		}
	}
	return s
}
