package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	archivepkg "github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/outbound"
)

func TestDemoOfflineAndHelp(t *testing.T) {
	var out, err bytes.Buffer
	if c := run([]string{"demo", "--no-color"}, &out, &err); c != 0 {
		t.Fatalf("%d %s", c, err.String())
	}
	s := out.String()
	if strings.Count(s, "duplicate") != 0 || !strings.Contains(s, "[YT]") || !strings.Contains(s, "[KICK]") || !strings.Contains(s, "↪") || !strings.Contains(s, "[PAID") {
		t.Fatal(s)
	}
	out.Reset()
	if c := run([]string{"--help"}, &out, &err); c != 0 || !strings.Contains(out.String(), "kick subscribe") || !strings.Contains(out.String(), "streamchat serve") || !strings.Contains(out.String(), "/kk hello") || !strings.Contains(out.String(), "Selection lasts for this run") {
		t.Fatal(out.String())
	}
}

func TestSetupArgumentsAcceptConfigAfterPlatform(t *testing.T) {
	selected, path, err := setupArguments([]string{"youtube-server", "--config", "/etc/streamchat/config.json"})
	if err != nil || len(selected) != 1 || selected[0] != "youtube-server" || path != "/etc/streamchat/config.json" {
		t.Fatalf("selected=%v path=%q err=%v", selected, path, err)
	}
}

type fakeAdapter struct {
	name    string
	message chat.Message
}

func (f fakeAdapter) Name() string { return f.name }
func (f fakeAdapter) Run(ctx context.Context, out chan<- chat.Message) error {
	select {
	case out <- f.message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRunMultipleAdapters(t *testing.T) {
	now := time.Now()
	adapters := []chat.Adapter{fakeAdapter{"youtube", chat.Message{ID: "1", Platform: chat.PlatformYouTube, Timestamp: now, AuthorDisplayName: "one", Text: "hello", EventType: chat.EventMessage}}, fakeAdapter{"twitch", chat.Message{ID: "2", Platform: chat.PlatformTwitch, Timestamp: now.Add(time.Millisecond), AuthorDisplayName: "two", Text: "world", EventType: chat.EventMessage}}}
	c := config.Defaults()
	c.NoColor = true
	var out, errw bytes.Buffer
	if err := runAdapters(context.Background(), adapters, c, nil, nil, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[YT]") || !strings.Contains(out.String(), "[TW]") {
		t.Fatalf("%s / %s", out.String(), errw.String())
	}
}

type blockingSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSender) Send(ctx context.Context, _ string) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type triggeredAdapter struct{ trigger <-chan struct{} }

func (a triggeredAdapter) Name() string { return "kick" }
func (a triggeredAdapter) Run(ctx context.Context, out chan<- chat.Message) error {
	select {
	case <-a.trigger:
	case <-ctx.Done():
		return ctx.Err()
	}
	message := chat.Message{ID: "incoming", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "viewer", Text: "still live", EventType: chat.EventMessage}
	select {
	case out <- message:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

type channelWriter struct{ writes chan string }

func (w channelWriter) Write(p []byte) (int, error) {
	w.writes <- string(p)
	return len(p), nil
}

func TestIncomingRendersWhileOutboundSendIsActive(t *testing.T) {
	sender := &blockingSender{started: make(chan struct{}), release: make(chan struct{})}
	targets := outbound.New(map[string]outbound.Sender{"kk": sender})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan string, 2)
	done := make(chan error, 1)
	c := config.Defaults()
	c.NoColor = true
	go func() {
		done <- runAdapters(ctx, []chat.Adapter{triggeredAdapter{trigger: sender.started}}, c, strings.NewReader("/kk sending\n"), targets, channelWriter{writes: writes}, io.Discard)
	}()
	select {
	case line := <-writes:
		if !strings.Contains(line, "still live") {
			t.Fatalf("unexpected render: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incoming chat was not rendered while outbound send was blocked")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAdapters did not stop")
	}
}

func TestOutboundInputDisplaysNoTargetInstruction(t *testing.T) {
	var errw bytes.Buffer
	runOutboundInput(context.Background(), strings.NewReader("hello\n"), outbound.New(map[string]outbound.Sender{"kk": &recordingOutboundSender{}}), &errw)
	if got := strings.TrimSpace(errw.String()); got != outbound.NoTargetInstruction {
		t.Fatalf("got %q", got)
	}
}

type recordingOutboundSender struct{ messages []string }

func (s *recordingOutboundSender) Send(_ context.Context, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestRemoteServerReplacesServerOwnedAdapters(t *testing.T) {
	c := config.Defaults()
	c.Client.ServerURL = "ws://127.0.0.1:8788/relay"
	c.RelayAuthToken = "0123456789abcdef0123456789abcdef"
	adapters := []chat.Adapter{
		fakeAdapter{name: "youtube"},
		fakeAdapter{name: "kick"},
		fakeAdapter{name: "twitch"},
	}
	got := useRemoteServer(adapters, c)
	names := make([]string, 0, len(got))
	for _, adapter := range got {
		names = append(names, adapter.Name())
	}
	if strings.Join(names, ",") != "twitch,server" {
		t.Fatalf("unexpected adapters: %v", names)
	}
}

func TestSafeErrorRedactsSecrets(t *testing.T) {
	s := safeError(errors.New("request?access_token=secret-token&client_secret=secret-client relay_auth_token=raw-relay-secret Authorization: Bearer header-relay-secret next"))
	for _, secret := range []string{"secret-token", "secret-client", "raw-relay-secret", "header-relay-secret"} {
		if strings.Contains(s, secret) {
			t.Fatal(s)
		}
	}
}

func TestConfigShowRedactsEverySecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.YouTube.APIKey = "youtube-private"
	c.YouTube.ClientSecret = "youtube-client-private"
	c.YouTube.RefreshToken = "youtube-refresh-private"
	c.Kick.ClientSecret = "kick-private"
	c.Twitch.AccessToken = "twitch-private"
	c.RelayAuthToken = "relay-private"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "show", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, s := range []string{"youtube-private", "youtube-client-private", "youtube-refresh-private", "kick-private", "twitch-private", "relay-private"} {
		if strings.Contains(out.String(), s) {
			t.Fatalf("leaked %s", s)
		}
	}
	if !strings.Contains(out.String(), "<redacted>") {
		t.Fatal(out.String())
	}
}
func TestEmptyConfigCheck(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, err bytes.Buffer
	if c := run([]string{"config", "check"}, &out, &err); c != 0 || !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("%d %s", c, err.String())
	}
}

func TestConfigCheckExplainsKickPortalWebhookRelationship(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.Kick.WebhookURL = "https://streamchat.sleepymario.com/webhooks/kick"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "check", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, want := range []string{
		"https://streamchat.sleepymario.com/webhooks/kick",
		"must match the webhook URL in the Kick developer portal",
		"local value is not sent to Kick",
		"streamchat kick subscribe",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("config check missing %q:\n%s", want, out.String())
		}
	}
}

func TestArchiveStatsCommandUsesSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "streamchat.db")
	store, err := archivepkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	message := chat.Message{ID: "yt-smoke", Platform: chat.PlatformYouTube, ChannelID: "broadcast", Timestamp: time.Now().UTC(), AuthorDisplayName: "viewer", Text: "persisted", EventType: chat.EventMessage}
	if inserted, storeErr := store.Store(context.Background(), message); storeErr != nil || !inserted {
		t.Fatalf("store=%v, %v", inserted, storeErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	c := config.Defaults()
	c.Storage.SQLitePath = dbPath
	if err = config.Save(configPath, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"archive", "stats", "--config", configPath}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "Messages: 1") || !strings.Contains(out.String(), "youtube: 1") {
		t.Fatal(out.String())
	}
}
