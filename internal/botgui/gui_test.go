package botgui

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SleepyMario/streamchat/internal/bot"
)

type sender struct{}

func (sender) SendTo(context.Context, string, string) error { return nil }

func TestRequiresPasswordOutsideLoopback(t *testing.T) {
	if _, err := New(Config{Listen: "0.0.0.0:8793", Engine: bot.New(sender{}, bot.Config{})}); err == nil {
		t.Fatal("public bind accepted without password")
	}
}

func TestOverlayIsReadOnlyWithoutControlPassword(t *testing.T) {
	engine := bot.New(sender{}, bot.Config{})
	server, err := New(Config{Listen: "10.77.0.1:8793", Password: "secret", Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/overlay/chat", "/api/overlay/chat", "/overlay.css", "/overlay.js"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("protected state=%d", w.Code)
	}
}

func TestOverlayScriptIgnoresKnownBots(t *testing.T) {
	engine := bot.New(sender{}, bot.Config{})
	server, err := New(Config{Listen: "127.0.0.1:8793", Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/overlay.js", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("overlay script=%d %s", w.Code, w.Body.String())
	}
	for _, author := range []string{"botrix", "kickbot"} {
		if !strings.Contains(w.Body.String(), `"`+author+`"`) {
			t.Fatalf("overlay ignore list is missing %q", author)
		}
	}
}

func TestUIAndSettings(t *testing.T) {
	engine := bot.New(sender{}, bot.Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5e9})
	saved := false
	server, err := New(Config{
		Listen: "127.0.0.1:8793", Engine: engine,
		Channels: func() any { return map[string]any{"twitch": map[string]any{"live": true, "available": true}} },
		Save:     func(bot.State) error { saved = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Streamchat Bot") {
		t.Fatalf("ui=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"channels":{"twitch":{"available":true,"live":true}}`) {
		t.Fatalf("state channels=%d %s", w.Code, w.Body.String())
	}
	body := bytes.NewBufferString(`{"enabled":false,"commands_reply":"Commands: !commands, !status","cooldown_seconds":9,"platforms":{"kick":true,"twitch":false}}`)
	r = httptest.NewRequest(http.MethodPut, "/api/settings", body)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !saved {
		t.Fatalf("settings=%d %s", w.Code, w.Body.String())
	}
	state := engine.State()
	if state.Enabled || state.Cooldown != 9 || state.Platforms["twitch"] {
		t.Fatalf("state=%+v", state)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"platform":"kick"}`))
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(engine.Activity()) != 1 {
		t.Fatalf("test=%d activity=%v", w.Code, engine.Activity())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/subtitles", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"disabled"`) {
		t.Fatalf("subtitle status=%d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/overlay/chat", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Streamchat overlay") {
		t.Fatalf("overlay=%d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/overlay/chat", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"recent_chat":[]`) {
		t.Fatalf("overlay chat=%d %s", w.Code, w.Body.String())
	}
}
