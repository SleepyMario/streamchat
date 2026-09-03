package bot

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

// EventKind is the provider-neutral trigger understood by the automation
// layer. Platform adapters translate their native webhook/API payloads into
// these events; provider-specific fields may remain in Event.Metadata.
type EventKind string

const (
	EventChatMessage        EventKind = "chat_message"
	EventFollow             EventKind = "follow"
	EventSubscription       EventKind = "subscription"
	EventGiftSubscription   EventKind = "gift_subscription"
	EventRaid               EventKind = "raid"
	EventDonation           EventKind = "donation"
	EventReward             EventKind = "reward"
	EventModeration         EventKind = "moderation"
	EventStreamStarted      EventKind = "stream_started"
	EventStreamHealthy      EventKind = "stream_healthy"
	EventStreamDisconnected EventKind = "stream_disconnected"
	EventStreamReconnected  EventKind = "stream_reconnected"
	EventStreamEnded        EventKind = "stream_ended"
	EventViewerThreshold    EventKind = "viewer_threshold"
	EventScheduled          EventKind = "scheduled"
	EventManual             EventKind = "manual"
)

// Event contains no credentials and is safe to pass to deterministic rules,
// the dashboard and (after explicit filtering) a conversational provider.
type Event struct {
	ID         string            `json:"id,omitempty"`
	Kind       EventKind         `json:"kind"`
	Platform   string            `json:"platform,omitempty"`
	ChannelID  string            `json:"channel_id,omitempty"`
	ActorID    string            `json:"actor_id,omitempty"`
	Actor      string            `json:"actor,omitempty"`
	Text       string            `json:"text,omitempty"`
	OccurredAt time.Time         `json:"occurred_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// EventFromChat translates the shared chat/alert model into the bot's
// provider-neutral event model.
func EventFromChat(message chat.Message) Event {
	event := Event{
		ID: message.ID, Kind: EventChatMessage, Platform: string(message.Platform),
		ChannelID: message.ChannelID, ActorID: message.AuthorID,
		Actor: message.AuthorDisplayName, Text: message.Text, OccurredAt: message.Timestamp,
	}
	if message.Platform != chat.PlatformTwitch {
		return event
	}
	switch message.SafePlatformMetadata["twitch_event"] {
	case "channel.follow":
		event.Kind = EventFollow
	case "channel.subscribe":
		event.Kind = EventSubscription
		if message.Membership != nil && message.Membership.IsGift {
			event.Kind = EventGiftSubscription
		}
	case "channel.subscription.gift":
		event.Kind = EventGiftSubscription
		if message.Membership != nil {
			event.Metadata = map[string]string{"gift_count": fmt.Sprint(message.Membership.GiftCount)}
		}
	}
	if message.EventType == chat.EventPaid && message.Paid != nil {
		event.Kind = EventDonation
		event.Metadata = map[string]string{"display": message.Paid.Display}
	}
	return event
}

type ScheduleMode string

const (
	ScheduleOnce      ScheduleMode = "once"
	ScheduleFixedTime ScheduleMode = "fixed_time"
	ScheduleInterval  ScheduleMode = "interval"
	ScheduleAfterLive ScheduleMode = "after_stream_start"
)

// Schedule is intentionally data-only for now. The future scheduler will
// persist next-run/last-run state rather than relying on process-local timers.
type Schedule struct {
	Mode     ScheduleMode  `json:"mode"`
	At       time.Time     `json:"at,omitempty"`
	Every    time.Duration `json:"every,omitempty"`
	Delay    time.Duration `json:"delay,omitempty"`
	TimeZone string        `json:"time_zone,omitempty"`
	LiveOnly bool          `json:"live_only,omitempty"`
}

type ActionKind string

const (
	ActionSendMessage  ActionKind = "send_message"
	ActionSendSequence ActionKind = "send_sequence"
	ActionShowAlert    ActionKind = "show_alert"
	ActionPlayMedia    ActionKind = "play_media"
	ActionArchive      ActionKind = "archive"
	ActionNotify       ActionKind = "notify_dashboard"
	ActionUtility      ActionKind = "internal_utility"
	ActionAIReply      ActionKind = "ai_reply"
)

type MessageStep struct {
	Delay   time.Duration `json:"delay,omitempty"`
	Message string        `json:"message"`
}

type Action struct {
	Kind     ActionKind        `json:"kind"`
	Platform string            `json:"platform,omitempty"`
	Message  string            `json:"message,omitempty"`
	Sequence []MessageStep     `json:"sequence,omitempty"`
	Target   string            `json:"target,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

type Trigger struct {
	Kind      EventKind `json:"kind"`
	Platforms []string  `json:"platforms,omitempty"`
	Command   string    `json:"command,omitempty"`
	Schedule  *Schedule `json:"schedule,omitempty"`
}

type RetryPolicy struct {
	Attempts int           `json:"attempts,omitempty"`
	Backoff  time.Duration `json:"backoff,omitempty"`
}

// Rule is the durable configuration boundary for deterministic commands,
// alerts, timed messages and optional conversational actions.
type Rule struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	Trigger      Trigger           `json:"trigger"`
	Conditions   map[string]string `json:"conditions,omitempty"`
	Actions      []Action          `json:"actions"`
	Cooldown     time.Duration     `json:"cooldown,omitempty"`
	DedupeWindow time.Duration     `json:"dedupe_window,omitempty"`
	Priority     int               `json:"priority,omitempty"`
	ValidFrom    time.Time         `json:"valid_from,omitempty"`
	ValidUntil   time.Time         `json:"valid_until,omitempty"`
	Retry        RetryPolicy       `json:"retry,omitempty"`
}

type ExecutionState struct {
	RuleID       string    `json:"rule_id"`
	LastRun      time.Time `json:"last_run,omitempty"`
	NextRun      time.Time `json:"next_run,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	RunCount     uint64    `json:"run_count"`
	FailureCount uint64    `json:"failure_count"`
}

// AuditEntry makes every automatic or AI-generated action explainable in the
// dashboard without storing credentials or raw model configuration.
type AuditEntry struct {
	Time      time.Time  `json:"time"`
	EventID   string     `json:"event_id,omitempty"`
	RuleID    string     `json:"rule_id,omitempty"`
	Platform  string     `json:"platform,omitempty"`
	Action    ActionKind `json:"action"`
	Summary   string     `json:"summary"`
	Succeeded bool       `json:"succeeded"`
	Error     string     `json:"error,omitempty"`
}

func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Name) == "" {
		return errors.New("automation rule requires an id and name")
	}
	if r.Trigger.Kind == "" {
		return errors.New("automation rule requires a trigger")
	}
	if len(r.Actions) == 0 {
		return errors.New("automation rule requires at least one action")
	}
	for _, action := range r.Actions {
		if action.Kind == "" {
			return errors.New("automation action requires a kind")
		}
	}
	return nil
}
