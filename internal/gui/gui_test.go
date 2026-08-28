package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientruntime"
	"github.com/SleepyMario/streamchat/internal/config"
)

func TestNewRequiresPasswordForPublicBind(t *testing.T) {
	if _, err := New(Config{Listen: "0.0.0.0:8792"}); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password guard, got %v", err)
	}
	if _, err := New(Config{Listen: "0.0.0.0:8792", Password: "private"}); err != nil {
		t.Fatal(err)
	}
}
func TestHandlerServesUIHealthAndSecurityHeaders(t *testing.T) {
	server, err := New(Config{Listen: "127.0.0.1:8792"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Streamchat") {
		t.Fatalf("unexpected UI response: %d", response.Code)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected health response: %d %q", response.Code, response.Body.String())
	}
}
func TestPasswordProtectsEveryEndpoint(t *testing.T) {
	server, err := New(Config{Listen: "0.0.0.0:8792", Password: "private"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	request.SetBasicAuth("streamchat", "private")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized status=%d", response.Code)
	}
}
func TestPublishRejectsInvalidMessages(t *testing.T) {
	server, err := New(Config{Listen: "127.0.0.1:8792"})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Publish(chat.Message{}); err == nil {
		t.Fatal("invalid message accepted")
	}
}

func TestRuntimeEndpointsExposeStateGuidanceAndControlledShutdown(t *testing.T) {
	cfg := config.Defaults()
	cfg.Path = filepath.Join(t.TempDir(), "config.json")
	cfg.Storage.SQLitePath = filepath.Join(t.TempDir(), "missing.db")
	if err := config.Save(cfg.Path, cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := clientruntime.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan struct{}, 1)
	server, err := New(Config{Runtime: runtime, Shutdown: func() { shutdown <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/state", "/api/config", "/api/setup"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var body any
		if json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("%s returned invalid JSON", path)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("shutdown status=%d", response.Code)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not requested")
	}
}
