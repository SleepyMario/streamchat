package clientruntime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/config"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	cfg := config.Defaults()
	cfg.Path = filepath.Join(t.TempDir(), "config.json")
	cfg.Storage.SQLitePath = filepath.Join(t.TempDir(), "archive.db")
	if err := config.Save(cfg.Path, cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestStateSelectionAndRelayAreFrontendNeutral(t *testing.T) {
	runtime := testRuntime(t)
	runtime.SetRelayState("connected")
	state := runtime.State()
	if state.Relay != "connected" {
		t.Fatalf("relay=%q", state.Relay)
	}
	if state.Targets["kick"] || state.Targets["twitch"] {
		t.Fatalf("unexpected targets: %#v", state.Targets)
	}
	if err := runtime.Select(context.Background(), "kick"); err == nil {
		t.Fatal("expected unavailable target error")
	}
}

func TestHealthAndSetupNeverExposeSecrets(t *testing.T) {
	runtime := testRuntime(t)
	runtime.cfg.RelayAuthToken = "top-secret"
	runtime.cfg.Kick.ClientSecret = "kick-secret"
	health := runtime.ConfigHealth()
	if health.Config.RelayAuthToken == "top-secret" || health.Config.Kick.ClientSecret == "kick-secret" {
		t.Fatal("health exposed a secret")
	}
	guides := runtime.SetupGuides()
	if len(guides) != 3 {
		t.Fatalf("guides=%d", len(guides))
	}
}

func TestArchiveStatsReadsExistingArchive(t *testing.T) {
	runtime := testRuntime(t)
	store, err := archive.Open(runtime.cfg.Storage.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Store(context.Background(), chat.Message{ID: "1", Platform: chat.PlatformKick, Timestamp: timeNow(), AuthorDisplayName: "a", Text: "hello", EventType: chat.EventMessage})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	stats, err := runtime.ArchiveStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("total=%d", stats.Total)
	}
}

func TestArchiveStatsDoesNotCreateMissingArchive(t *testing.T) {
	runtime := testRuntime(t)
	_ = os.Remove(runtime.cfg.Storage.SQLitePath)
	if _, err := runtime.ArchiveStats(context.Background()); err == nil {
		t.Fatal("expected missing archive error")
	}
	if _, err := os.Stat(runtime.cfg.Storage.SQLitePath); !os.IsNotExist(err) {
		t.Fatal("archive was created by inspection")
	}
}

func TestSharedRuntimeSelectsAndSendsThroughKickProvider(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		received = string(body)
		_, _ = io.WriteString(w, `{"data":{"is_sent":true,"message_id":"00000000-0000-0000-0000-000000000001"}}`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Path = filepath.Join(t.TempDir(), "config.json")
	cfg.Kick.AccessToken = "token"
	cfg.Kick.BroadcasterID = "123"
	cfg.Kick.APIBaseURL = server.URL
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Select(context.Background(), "kick"); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Execute(context.Background(), "hello from GUI and CLI"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, `"content":"hello from GUI and CLI"`) {
		t.Fatalf("body=%s", received)
	}
}

func TestSharedRuntimeUsesAuthenticatedServerForYouTube(t *testing.T) {
	var action string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/control" || r.Header.Get("Authorization") != "Bearer relay-token" {
			t.Fatalf("request=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		action = string(body)
		_, _ = io.WriteString(w, `{"result":"sent","message_id":"yt-1"}`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Path = filepath.Join(t.TempDir(), "config.json")
	cfg.Client.ServerURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/relay"
	cfg.RelayAuthToken = "relay-token"
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Select(context.Background(), "youtube"); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Execute(context.Background(), "hello YouTube"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(action, `"action":"send"`) || !strings.Contains(action, `"text":"hello YouTube"`) {
		t.Fatalf("body=%s", action)
	}
}

func TestYouTubeClearUsesAuthoritativeArchiveWithoutDeletingHistory(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/liveChat/messages" {
			deleted = append(deleted, r.URL.Query().Get("id"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("request=%s %s", r.Method, r.URL.String())
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Path = filepath.Join(dir, "config.json")
	cfg.Storage.SQLitePath = filepath.Join(dir, "archive.db")
	cfg.YouTube.BaseURL = server.URL
	cfg.YouTube.ClientID = "id"
	cfg.YouTube.ClientSecret = "secret"
	cfg.YouTube.RefreshToken = "refresh"
	cfg.YouTube.AccessToken = "access"
	if err := config.Save(cfg.Path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(cfg.Storage.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Store(context.Background(), chat.Message{ID: "yt-delete", Platform: chat.PlatformYouTube, Timestamp: time.Now(), AuthorID: "UC1", AuthorDisplayName: "Ada", Text: "x", EventType: chat.EventMessage})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Execute(context.Background(), "/clear youtube")
	if err != nil || !strings.Contains(result, "1 deleted") || len(deleted) != 1 || deleted[0] != "yt-delete" {
		t.Fatalf("result=%q deleted=%v err=%v", result, deleted, err)
	}
	stats, err := runtime.ArchiveStats(context.Background())
	if err != nil || stats.Total != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestBotConfigUsesSeparateAccountsWithoutChangingTargets(t *testing.T) {
	cfg := config.Defaults()
	cfg.Kick.BroadcasterID = "123"
	cfg.Kick.AccessToken = "owner-kick"
	cfg.Twitch.Channel = "owner-channel"
	cfg.Twitch.AccessToken = "owner-twitch"
	cfg.YouTube.VideoID = "owner-video"
	cfg.YouTube.AccessToken = "owner-youtube"
	cfg.Bot.Kick = config.BotAccount{ClientID: "bot-kick-client", AccessToken: "bot-kick"}
	cfg.Bot.Twitch = config.BotAccount{ClientID: "bot-twitch-client", AccessToken: "bot-twitch", UserID: "bot-user"}
	cfg.Bot.YouTube = config.BotAccount{ClientID: "bot-youtube-client", AccessToken: "bot-youtube", UserID: "bot-channel"}
	got := botConfig(cfg)
	if got.Kick.AccessToken != "bot-kick" || got.Kick.BroadcasterID != "123" {
		t.Fatalf("Kick bot identity or target lost: %+v", got.Kick)
	}
	if got.Twitch.AccessToken != "bot-twitch" || got.Twitch.Channel != "owner-channel" || got.Twitch.UserID != "bot-user" {
		t.Fatalf("Twitch bot identity or target lost: %+v", got.Twitch)
	}
	if got.YouTube.AccessToken != "bot-youtube" || got.YouTube.VideoID != "owner-video" {
		t.Fatalf("YouTube bot identity or target lost: %+v", got.YouTube)
	}
	if cfg.Kick.AccessToken != "owner-kick" || cfg.Twitch.AccessToken != "owner-twitch" || cfg.YouTube.AccessToken != "owner-youtube" {
		t.Fatal("source configuration was mutated")
	}
}

func TestBotConfigFallsBackPerPlatform(t *testing.T) {
	cfg := config.Defaults()
	cfg.Kick.AccessToken = "owner-kick"
	cfg.Twitch.AccessToken = "owner-twitch"
	cfg.YouTube.AccessToken = "owner-youtube"
	cfg.Bot.Kick.AccessToken = "bot-kick"
	got := botConfig(cfg)
	if got.Kick.AccessToken != "bot-kick" || got.Twitch.AccessToken != "owner-twitch" || got.YouTube.AccessToken != "owner-youtube" {
		t.Fatalf("unexpected fallback: kick=%q twitch=%q youtube=%q", got.Kick.AccessToken, got.Twitch.AccessToken, got.YouTube.AccessToken)
	}
}

func timeNow() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
