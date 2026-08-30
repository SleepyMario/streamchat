package bot

import (
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func TestEventFromChatProducesCredentialFreeNormalizedEvent(t *testing.T) {
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	event := EventFromChat(chat.Message{ID: "m1", Platform: chat.PlatformTwitch, ChannelID: "c1", AuthorID: "u1", AuthorDisplayName: "viewer", Text: "hello", Timestamp: now})
	if event.Kind != EventChatMessage || event.Platform != "twitch" || event.ChannelID != "c1" || event.Actor != "viewer" || event.Text != "hello" || !event.OccurredAt.Equal(now) {
		t.Fatalf("event=%+v", event)
	}
}

func TestRuleValidationKeepsPlaceholdersOutOfRuntime(t *testing.T) {
	valid := Rule{ID: "commands", Name: "Commands", Enabled: true, Trigger: Trigger{Kind: EventChatMessage, Command: "!commands"}, Actions: []Action{{Kind: ActionSendMessage, Message: "Commands: !commands"}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := Rule{ID: "follow", Name: "Follow placeholder", Trigger: Trigger{Kind: EventFollow}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("actionless placeholder was accepted as an executable rule")
	}
}
