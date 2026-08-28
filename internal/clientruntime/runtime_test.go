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

func timeNow() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
