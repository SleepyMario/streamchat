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

type recordingCommandLog struct{ records []CommandRecord }

func (l *recordingCommandLog) WriteCommand(record CommandRecord) error {
	l.records = append(l.records, record)
	return nil
}

func (s *recordingSender) SendTo(_ context.Context, platform, message string) error {
	s.platforms = append(s.platforms, platform)
	s.messages = append(s.messages, message)
	return nil
}

func TestCommandsOnEverySupportedPlatform(t *testing.T) {
	sender := &recordingSender{}
	commandLog := &recordingCommandLog{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5 * time.Second, CommandLog: commandLog})
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
	if len(sender.messages) != 3 || sender.platforms[0] != string(chat.PlatformKick) || sender.platforms[1] != string(chat.PlatformTwitch) || sender.platforms[2] != string(chat.PlatformYouTube) {
		t.Fatalf("platforms=%v messages=%v", sender.platforms, sender.messages)
	}
	if len(commandLog.records) != 3 || commandLog.records[0].Command != "!commands" || commandLog.records[1].Command != "!commands" || commandLog.records[2].Command != "!commands" {
		t.Fatalf("command records=%+v", commandLog.records)
	}
}

func TestCommandsCooldownIsPerPlatformAndChannel(t *testing.T) {
	sender := &recordingSender{}
	commandLog := &recordingCommandLog{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5 * time.Second, CommandLog: commandLog})
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
	if len(commandLog.records) != 3 {
		t.Fatalf("only executed commands should be recorded: %+v", commandLog.records)
	}
}

func TestDifferentCommandsDoNotShareCooldown(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands, !language", Cooldown: time.Minute})
	engine.now = func() time.Time { return time.Unix(100, 0) }
	for _, command := range []string{"!commands", "!language"} {
		if err := engine.handle(context.Background(), chat.Message{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: command, EventType: chat.EventMessage}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.messages) != 2 {
		t.Fatalf("different commands must both answer immediately: %v", sender.messages)
	}
}

func TestDuplicateCommandMayAnswerAfterFourInterveningMessages(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands, !language", Cooldown: time.Minute})
	engine.now = func() time.Time { return time.Unix(100, 0) }
	command := chat.Message{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: "!commands", EventType: chat.EventMessage}
	_ = engine.handle(context.Background(), command)
	_ = engine.handle(context.Background(), command)
	for range 3 {
		_ = engine.handle(context.Background(), chat.Message{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: "ordinary chat", EventType: chat.EventMessage})
	}
	_ = engine.handle(context.Background(), command)
	if len(sender.messages) != 1 {
		t.Fatalf("three intervening messages must still suppress the duplicate: %v", sender.messages)
	}
	_ = engine.handle(context.Background(), chat.Message{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: "fourth ordinary message", EventType: chat.EventMessage})
	_ = engine.handle(context.Background(), command)
	if len(sender.messages) != 2 {
		t.Fatalf("four intervening messages must allow the duplicate: %v", sender.messages)
	}
}

func TestLanguageCommandIsSharedAcrossAllPlatformsAndRecorded(t *testing.T) {
	sender := &recordingSender{}
	commandLog := &recordingCommandLog{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5 * time.Second, CommandLog: commandLog})
	engine.now = func() time.Time { return time.Unix(100, 0) }
	for _, message := range []chat.Message{
		{Platform: chat.PlatformTwitch, ChannelID: "twitch", Text: " !LANGUAGE ", EventType: chat.EventMessage},
		{Platform: chat.PlatformKick, ChannelID: "kick", Text: "!language", EventType: chat.EventMessage},
		{Platform: chat.PlatformYouTube, ChannelID: "youtube", Text: "!language", EventType: chat.EventMessage},
	} {
		if err := engine.handle(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.messages) != 3 || sender.platforms[0] != "twitch" || sender.platforms[1] != "kick" || sender.platforms[2] != "youtube" || sender.messages[0] != languageReply || sender.messages[1] != languageReply || sender.messages[2] != youtubeLanguageReply {
		t.Fatalf("platforms=%v messages=%v", sender.platforms, sender.messages)
	}
	if len(commandLog.records) != 3 || commandLog.records[0].Command != "!language" || commandLog.records[1].Command != "!language" || commandLog.records[2].Command != "!language" || !commandLog.records[0].Succeeded || !commandLog.records[1].Succeeded || !commandLog.records[2].Succeeded {
		t.Fatalf("command records=%+v", commandLog.records)
	}
}

func TestYouTubeSerializesDifferentRepliesAcrossCooldownWindow(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands, !language", Cooldown: time.Minute})
	now := time.Unix(100, 0)
	engine.now = func() time.Time { return now }
	var waited time.Duration
	engine.sleep = func(_ context.Context, duration time.Duration) error {
		waited = duration
		now = now.Add(duration)
		return nil
	}
	for _, command := range []string{"!commands", "!language"} {
		if err := engine.handle(context.Background(), chat.Message{Platform: chat.PlatformYouTube, ChannelID: "video", Text: command, EventType: chat.EventMessage}); err != nil {
			t.Fatal(err)
		}
	}
	if waited != time.Minute || len(sender.messages) != 2 || sender.messages[1] != youtubeLanguageReply {
		t.Fatalf("waited=%s messages=%v", waited, sender.messages)
	}
}

type recordingChannelSender struct {
	platform string
	channel  string
	message  string
}

func (s *recordingChannelSender) SendTo(context.Context, string, string) error {
	return nil
}

func (s *recordingChannelSender) SendToChannel(_ context.Context, platform, channel, message string) error {
	s.platform, s.channel, s.message = platform, channel, message
	return nil
}

func TestYouTubeCommandRepliesToOriginatingBroadcast(t *testing.T) {
	sender := &recordingChannelSender{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands, !language", Cooldown: time.Minute})
	err := engine.handle(context.Background(), chat.Message{Platform: chat.PlatformYouTube, ChannelID: "youtube-video-id", Text: "!commands", EventType: chat.EventMessage})
	if err != nil {
		t.Fatal(err)
	}
	if sender.platform != "youtube" || sender.channel != "youtube-video-id" || sender.message != "Commands: !commands, !language" {
		t.Fatalf("platform=%q channel=%q message=%q", sender.platform, sender.channel, sender.message)
	}
}

func TestPlatformAlertsSendSimpleChatAcknowledgements(t *testing.T) {
	sender := &recordingSender{}
	commandLog := &recordingCommandLog{}
	engine := New(sender, Config{Enabled: true, CommandsReply: "Commands: !commands", CommandLog: commandLog})
	tests := []struct {
		event Event
		want  string
	}{
		{Event{Kind: EventFollow, Platform: "twitch", Actor: "Follower"}, "Thanks for the follow, Follower!"},
		{Event{Kind: EventSubscription, Platform: "twitch", Actor: "Subscriber"}, "Thanks for the subscription, Subscriber!"},
		{Event{Kind: EventGiftSubscription, Platform: "twitch", Actor: "Viewer"}, "Enjoy the gift subscription, Viewer!"},
		{Event{Kind: EventGiftSubscription, Platform: "twitch", Actor: "Gifter", Metadata: map[string]string{"gift_count": "5"}}, "Thanks for giving 5 gift subs, Gifter!"},
		{Event{Kind: EventGiftSubscription, Platform: "twitch", Actor: "Gifter", Metadata: map[string]string{"gift_count": "1"}}, "Thanks for giving 1 gift sub, Gifter!"},
		{Event{Kind: EventDonation, Platform: "twitch", Actor: "Cheerer", Metadata: map[string]string{"display": "100 Bits"}}, "Thanks for the 100 Bits, Cheerer!"},
		{Event{Kind: EventFollow, Platform: "kick", Actor: "Follower"}, "Thanks for the follow, Follower!"},
		{Event{Kind: EventSubscription, Platform: "kick", Actor: "Subscriber"}, "Thanks for the subscription, Subscriber!"},
		{Event{Kind: EventGiftSubscription, Platform: "kick", Actor: "Gifter", Metadata: map[string]string{"gift_count": "5"}}, "Thanks for giving 5 gift subs, Gifter!"},
		{Event{Kind: EventDonation, Platform: "kick", Actor: "Supporter", Metadata: map[string]string{"display": "500 KICKs"}}, "Thanks for the 500 KICKs, Supporter!"},
	}
	for _, test := range tests {
		if err := engine.handleEvent(context.Background(), test.event); err != nil {
			t.Fatal(err)
		}
		if got := sender.messages[len(sender.messages)-1]; got != test.want {
			t.Fatalf("message=%q want=%q", got, test.want)
		}
	}
	if len(commandLog.records) != 0 {
		t.Fatalf("alerts must not enter command-only log: %+v", commandLog.records)
	}
}

func TestPlatformAlertsRespectPlatformDisableAndIgnoreYouTube(t *testing.T) {
	sender := &recordingSender{}
	engine := New(sender, Config{Enabled: true, Disabled: map[string]bool{"twitch": true, "kick": true}})
	_ = engine.handleEvent(context.Background(), Event{Kind: EventFollow, Platform: "twitch", Actor: "Follower"})
	_ = engine.handleEvent(context.Background(), Event{Kind: EventFollow, Platform: "kick", Actor: "Follower"})
	_ = engine.handleEvent(context.Background(), Event{Kind: EventFollow, Platform: "youtube", Actor: "Follower"})
	if len(sender.messages) != 0 {
		t.Fatalf("unexpected messages=%v", sender.messages)
	}
}

func TestUpdatePreservesIndependentYouTubePlatformState(t *testing.T) {
	engine := New(&recordingSender{}, Config{})
	engine.Update(State{Platforms: map[string]bool{"kick": true, "twitch": false, "youtube": true}, Cooldown: 60})
	state := engine.State()
	if !state.Platforms["kick"] || state.Platforms["twitch"] || !state.Platforms["youtube"] {
		t.Fatalf("platform state=%v", state.Platforms)
	}
}
