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

func TestEventFromChatMapsTwitchAlerts(t *testing.T) {
	tests := []struct {
		name    string
		message chat.Message
		want    EventKind
		display string
	}{
		{"follow", chat.Message{Platform: chat.PlatformTwitch, SafePlatformMetadata: map[string]string{"twitch_event": "channel.follow"}}, EventFollow, ""},
		{"subscription", chat.Message{Platform: chat.PlatformTwitch, SafePlatformMetadata: map[string]string{"twitch_event": "channel.subscribe"}, Membership: &chat.Membership{}}, EventSubscription, ""},
		{"gift subscription", chat.Message{Platform: chat.PlatformTwitch, SafePlatformMetadata: map[string]string{"twitch_event": "channel.subscribe"}, Membership: &chat.Membership{IsGift: true}}, EventGiftSubscription, ""},
		{"gift subscription batch", chat.Message{Platform: chat.PlatformTwitch, SafePlatformMetadata: map[string]string{"twitch_event": "channel.subscription.gift"}, Membership: &chat.Membership{GiftCount: 5, IsGift: true}}, EventGiftSubscription, ""},
		{"bits", chat.Message{Platform: chat.PlatformTwitch, EventType: chat.EventPaid, Paid: &chat.Paid{Display: "100 Bits"}}, EventDonation, "100 Bits"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := EventFromChat(test.message)
			if event.Kind != test.want || event.Metadata["display"] != test.display {
				t.Fatalf("event=%+v", event)
			}
			if test.name == "gift subscription batch" && event.Metadata["gift_count"] != "5" {
				t.Fatalf("event=%+v", event)
			}
		})
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
