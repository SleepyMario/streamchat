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

type channelSender interface {
	SendToChannel(context.Context, string, string, string) error
}

const languageReply = "You can try Nederlands, English, Deutsch, 中文, 한국말, 日本語 and tiếng Việt on that fat Sleepy dude."
const youtubeLanguageReply = "You can try Nederlands, English, Deutsch, 中文, 한국말, 日本語 and tiếng Việt on that Sleepy dude."

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

type commandReplyState struct {
	at       time.Time
	sequence uint64
}

type Engine struct {
	sender     Sender
	cfg        Config
	queue      chan Event
	now        func() time.Time
	mu         sync.Mutex
	last       map[string]commandReplyState
	sequences  map[string]uint64
	activity   []Activity
	commandLog CommandRecorder
}

func New(sender Sender, cfg Config) *Engine {
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 32
	}
	return &Engine{sender: sender, cfg: cfg, queue: make(chan Event, cfg.QueueSize), now: time.Now, last: make(map[string]commandReplyState), sequences: make(map[string]uint64), commandLog: cfg.CommandLog}
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
	if event.Platform == string(chat.PlatformTwitch) || event.Platform == string(chat.PlatformKick) {
		if reply, activity := platformAlertReply(event); reply != "" {
			if err := e.sender.SendTo(ctx, event.Platform, reply); err != nil {
				e.record(event.Platform, activity+" failed", true)
				return fmt.Errorf("send %s on %s: %w", strings.ToLower(activity), event.Platform, err)
			}
			e.record(event.Platform, activity, false)
			return nil
		}
	}
	if event.Kind != EventChatMessage {
		return nil
	}
	channelKey := event.Platform + "\x00" + event.ChannelID
	command := strings.ToLower(strings.TrimSpace(event.Text))
	reply := ""
	switch command {
	case "!commands":
		if supportedCommandPlatform(event.Platform) {
			reply = cfg.CommandsReply
		}
	case "!language":
		if supportedCommandPlatform(event.Platform) {
			reply = languageReply
			if event.Platform == string(chat.PlatformYouTube) {
				reply = youtubeLanguageReply
			}
		}
	}
	if reply == "" {
		e.mu.Lock()
		e.sequences[channelKey]++
		e.mu.Unlock()
		return nil
	}
	key := channelKey + "\x00" + command
	now := e.now()
	e.mu.Lock()
	sequence := e.sequences[channelKey]
	last := e.last[key]
	messagesBetween := uint64(0)
	if sequence > last.sequence {
		messagesBetween = sequence - last.sequence
	}
	if !last.at.IsZero() && now.Sub(last.at) < cfg.Cooldown && messagesBetween < 4 {
		e.mu.Unlock()
		return nil
	}
	e.last[key] = commandReplyState{at: now, sequence: sequence}
	e.mu.Unlock()
	if err := e.sendReply(ctx, event, reply); err != nil {
		e.record(event.Platform, command+" reply failed", true)
		if logErr := e.recordCommand(event.Platform, command, false); logErr != nil {
			return errors.Join(fmt.Errorf("answer %s on %s: %w", command, event.Platform, err), logErr)
		}
		return fmt.Errorf("answer %s on %s: %w", command, event.Platform, err)
	}
	e.record(event.Platform, "Answered "+command, false)
	return e.recordCommand(event.Platform, command, true)
}

func supportedCommandPlatform(platform string) bool {
	return platform == string(chat.PlatformKick) || platform == string(chat.PlatformTwitch) || platform == string(chat.PlatformYouTube)
}

func (e *Engine) sendReply(ctx context.Context, event Event, reply string) error {
	if sender, ok := e.sender.(channelSender); ok {
		return sender.SendToChannel(ctx, event.Platform, event.ChannelID, reply)
	}
	return e.sender.SendTo(ctx, event.Platform, reply)
}

func platformAlertReply(event Event) (string, string) {
	actor := strings.TrimSpace(event.Actor)
	if actor == "" {
		actor = "someone"
	}
	switch event.Kind {
	case EventFollow:
		return fmt.Sprintf("Thanks for the follow, %s!", actor), "Sent follow thank-you"
	case EventSubscription:
		return fmt.Sprintf("Thanks for the subscription, %s!", actor), "Sent subscription thank-you"
	case EventGiftSubscription:
		if count := strings.TrimSpace(event.Metadata["gift_count"]); count != "" && count != "0" {
			unit := "gift subs"
			if count == "1" {
				unit = "gift sub"
			}
			return fmt.Sprintf("Thanks for giving %s %s, %s!", count, unit, actor), "Sent gift-subscription thank-you"
		}
		return fmt.Sprintf("Enjoy the gift subscription, %s!", actor), "Sent gift-subscription message"
	case EventDonation:
		amount := strings.TrimSpace(event.Metadata["display"])
		if amount == "" {
			amount = "Bits"
		}
		return fmt.Sprintf("Thanks for the %s, %s!", amount, actor), "Sent donation thank-you"
	default:
		return "", ""
	}
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
	if platform != "kick" && platform != "twitch" && platform != "youtube" {
		return fmt.Errorf("test platform must be kick, twitch, or youtube")
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
	return State{Enabled: e.cfg.Enabled, ShowChat: e.cfg.ShowChat, CommandsReply: e.cfg.CommandsReply, Cooldown: int(e.cfg.Cooldown / time.Second), Platforms: map[string]bool{"kick": !e.cfg.Disabled["kick"], "twitch": !e.cfg.Disabled["twitch"], "youtube": !e.cfg.Disabled["youtube"]}}
}

func (e *Engine) Update(state State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.Enabled = state.Enabled
	e.cfg.ShowChat = state.ShowChat
	e.cfg.CommandsReply = strings.TrimSpace(state.CommandsReply)
	e.cfg.Cooldown = time.Duration(state.Cooldown) * time.Second
	e.cfg.Disabled = map[string]bool{"kick": !state.Platforms["kick"], "twitch": !state.Platforms["twitch"], "youtube": !state.Platforms["youtube"]}
}
