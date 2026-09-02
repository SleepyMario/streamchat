package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

type Sender interface {
	SendTo(context.Context, string, string) error
}

type Config struct {
	Enabled       bool
	ShowChat      bool
	CommandsReply string
	Cooldown      time.Duration
	QueueSize     int
	Disabled      map[string]bool
	CommandLog    CommandRecorder
}

type State struct {
	Enabled       bool            `json:"enabled"`
	ShowChat      bool            `json:"show_chat"`
	CommandsReply string          `json:"commands_reply"`
	Cooldown      int             `json:"cooldown_seconds"`
	Platforms     map[string]bool `json:"platforms"`
}

type Activity struct {
	Time     time.Time `json:"time"`
	Platform string    `json:"platform"`
	Message  string    `json:"message"`
	Error    bool      `json:"error"`
}

type Engine struct {
	sender     Sender
	cfg        Config
	queue      chan Event
	now        func() time.Time
	mu         sync.Mutex
	last       map[string]time.Time
	activity   []Activity
	commandLog CommandRecorder
}

func New(sender Sender, cfg Config) *Engine {
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 32
	}
	return &Engine{sender: sender, cfg: cfg, queue: make(chan Event, cfg.QueueSize), now: time.Now, last: make(map[string]time.Time), commandLog: cfg.CommandLog}
}

// Enqueue is deliberately non-blocking: bot traffic may never stall chat relay.
func (e *Engine) Enqueue(message chat.Message) bool {
	return e.EnqueueEvent(EventFromChat(message))
}

// EnqueueEvent is the provider-neutral entry point shared by present chat
// commands and future follow, alert, schedule and conversational triggers.
func (e *Engine) EnqueueEvent(event Event) bool {
	select {
	case e.queue <- event:
		return true
	default:
		return false
	}
}

func (e *Engine) Run(ctx context.Context, report func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-e.queue:
			if err := e.handleEvent(ctx, event); err != nil && report != nil {
				report(err)
			}
		}
	}
}

func (e *Engine) handle(ctx context.Context, message chat.Message) error {
	return e.handleEvent(ctx, EventFromChat(message))
}

func (e *Engine) handleEvent(ctx context.Context, event Event) error {
	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()
	if !cfg.Enabled || cfg.Disabled[event.Platform] {
		return nil
	}
	if event.Kind != EventChatMessage || !strings.EqualFold(strings.TrimSpace(event.Text), "!commands") {
		return nil
	}
	if event.Platform != string(chat.PlatformKick) && event.Platform != string(chat.PlatformTwitch) {
		return nil
	}
	key := event.Platform + "\x00" + event.ChannelID
	now := e.now()
	e.mu.Lock()
	last := e.last[key]
	if !last.IsZero() && now.Sub(last) < cfg.Cooldown {
		e.mu.Unlock()
		return nil
	}
	e.last[key] = now
	e.mu.Unlock()
	if err := e.sender.SendTo(ctx, event.Platform, cfg.CommandsReply); err != nil {
		e.record(event.Platform, "!commands reply failed", true)
		if logErr := e.recordCommand(event.Platform, "!commands", false); logErr != nil {
			return errors.Join(fmt.Errorf("answer !commands on %s: %w", event.Platform, err), logErr)
		}
		return fmt.Errorf("answer !commands on %s: %w", event.Platform, err)
	}
	e.record(event.Platform, "Answered !commands", false)
	return e.recordCommand(event.Platform, "!commands", true)
}

func (e *Engine) recordCommand(platform, command string, succeeded bool) error {
	if e.commandLog == nil {
		return nil
	}
	if err := e.commandLog.WriteCommand(CommandRecord{Time: e.now().UTC(), Platform: platform, Command: command, Succeeded: succeeded}); err != nil {
		return fmt.Errorf("record executed bot command: %w", err)
	}
	return nil
}

func (e *Engine) Test(ctx context.Context, platform string) error {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "kick" && platform != "twitch" {
		return fmt.Errorf("test platform must be kick or twitch")
	}
	e.mu.Lock()
	reply := e.cfg.CommandsReply
	e.mu.Unlock()
	if err := e.sender.SendTo(ctx, platform, reply); err != nil {
		e.record(platform, "Test reply failed", true)
		return err
	}
	e.record(platform, "Sent test reply", false)
	return nil
}

func (e *Engine) Activity() []Activity {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Activity(nil), e.activity...)
}

func (e *Engine) record(platform, message string, failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activity = append([]Activity{{Time: time.Now().UTC(), Platform: platform, Message: message, Error: failed}}, e.activity...)
	if len(e.activity) > 50 {
		e.activity = e.activity[:50]
	}
}

func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return State{Enabled: e.cfg.Enabled, ShowChat: e.cfg.ShowChat, CommandsReply: e.cfg.CommandsReply, Cooldown: int(e.cfg.Cooldown / time.Second), Platforms: map[string]bool{"kick": !e.cfg.Disabled["kick"], "twitch": !e.cfg.Disabled["twitch"]}}
}

func (e *Engine) Update(state State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.Enabled = state.Enabled
	e.cfg.ShowChat = state.ShowChat
	e.cfg.CommandsReply = strings.TrimSpace(state.CommandsReply)
	e.cfg.Cooldown = time.Duration(state.Cooldown) * time.Second
	e.cfg.Disabled = map[string]bool{"kick": !state.Platforms["kick"], "twitch": !state.Platforms["twitch"]}
}
