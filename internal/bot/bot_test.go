package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

type recordingSender struct {
	platforms []string
	messages  []string
}

func (s *recordingSender) SendTo(_ context.Context, platform, message string) error {
	s.platforms = append(s.platforms, platform)
	s.messages = append(s.messages, message)
	return nil
}

func TestCommandsOnKickAndTwitchOnly(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5 * time.Second})
	engine.now = func() time.Time { return time.Unix(100, 0) }
	for _, message := range []chat.Message{
		{Platform: chat.PlatformKick, ChannelID: "kick", Text: " !COMMANDS ", EventType: chat.EventMessage},
		{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: "!commands", EventType: chat.EventMessage},
		{Platform: chat.PlatformYouTube, ChannelID: "youtube", Text: "!commands", EventType: chat.EventMessage},
		{Platform: chat.PlatformKick, ChannelID: "kick", Text: "hello", EventType: chat.EventMessage},
	} {
		if err := engine.handle(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.messages) != 2 || sender.platforms[0] != string(chat.PlatformKick) || sender.platforms[1] != string(chat.PlatformTwitch) {
		t.Fatalf("platforms=%v messages=%v", sender.platforms, sender.messages)
	}
}

func TestCommandsCooldownIsPerPlatformAndChannel(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5 * time.Second})
	now := time.Unix(100, 0)
	engine.now = func() time.Time { return now }
	message := chat.Message{Platform: chat.PlatformKick, ChannelID: "one", Text: "!commands", EventType: chat.EventMessage}
	_ = engine.handle(context.Background(), message)
	_ = engine.handle(context.Background(), message)
	message.ChannelID = "two"
	_ = engine.handle(context.Background(), message)
	now = now.Add(6 * time.Second)
	message.ChannelID = "one"
	_ = engine.handle(context.Background(), message)
	if len(sender.messages) != 3 {
		t.Fatalf("sent=%d", len(sender.messages))
	}
}
