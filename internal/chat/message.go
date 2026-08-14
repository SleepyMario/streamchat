package chat

import (
	"context"
	"errors"
	"time"
)

type Platform string

const (
	PlatformYouTube Platform = "youtube"
	PlatformKick    Platform = "kick"
	PlatformTwitch  Platform = "twitch"
)

type EventType string

const (
	EventMessage    EventType = "message"
	EventPaid       EventType = "paid"
	EventMembership EventType = "membership"
	EventModeration EventType = "moderation"
	EventSystem     EventType = "system"
	EventOther      EventType = "other"
)

type Badge struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Count int    `json:"count,omitempty"`
}
type Emote struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
}
type Reply struct {
	MessageID         string `json:"message_id,omitempty"`
	AuthorID          string `json:"author_id,omitempty"`
	AuthorDisplayName string `json:"author_display_name,omitempty"`
	Text              string `json:"text,omitempty"`
}
type Paid struct {
	AmountMicros int64  `json:"amount_micros,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Display      string `json:"display,omitempty"`
}
type Membership struct {
	Level     string `json:"level,omitempty"`
	Months    int    `json:"months,omitempty"`
	GiftCount int    `json:"gift_count,omitempty"`
	IsGift    bool   `json:"is_gift,omitempty"`
}

// Message is the platform-neutral, credential-free event passed to frontends.
type Message struct {
	ID                   string            `json:"id"`
	Platform             Platform          `json:"platform"`
	ChannelID            string            `json:"channel_id,omitempty"`
	ChannelDisplayName   string            `json:"channel_display_name,omitempty"`
	Timestamp            time.Time         `json:"timestamp"`
	AuthorID             string            `json:"author_id,omitempty"`
	AuthorDisplayName    string            `json:"author_display_name,omitempty"`
	AuthorColor          string            `json:"author_color,omitempty"`
	Badges               []Badge           `json:"badges,omitempty"`
	Roles                []string          `json:"roles,omitempty"`
	Text                 string            `json:"text,omitempty"`
	Emotes               []Emote           `json:"emotes,omitempty"`
	Reply                *Reply            `json:"reply,omitempty"`
	EventType            EventType         `json:"event_type"`
	Paid                 *Paid             `json:"paid,omitempty"`
	Membership           *Membership       `json:"membership,omitempty"`
	SafePlatformMetadata map[string]string `json:"platform_metadata,omitempty"`
}

func (m Message) Validate() error {
	if m.ID == "" {
		return errors.New("message ID is required")
	}
	if m.Platform != PlatformYouTube && m.Platform != PlatformKick && m.Platform != PlatformTwitch {
		return errors.New("unsupported platform")
	}
	if m.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if m.EventType == "" {
		return errors.New("event type is required")
	}
	return nil
}

type ErrorKind int

const (
	Recoverable ErrorKind = iota + 1
	Terminal
	Authentication
)

type AdapterError struct {
	Kind ErrorKind
	Op   string
	Err  error
}

func (e *AdapterError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *AdapterError) Unwrap() error { return e.Err }

type Adapter interface {
	Name() string
	Run(context.Context, chan<- Message) error
}
