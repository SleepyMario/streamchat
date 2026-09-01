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
func TestUIAndSettings(t *testing.T) {
	engine := bot.New(sender{}, bot.Config{Enabled: true, CommandsReply: "Commands: !commands", Cooldown: 5e9})
	saved := false
	server, err := New(Config{Listen: "127.0.0.1:8793", Engine: engine, Save: func(bot.State) error { saved = true; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Streamchat Bot") {
		t.Fatalf("ui=%d", w.Code)
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
}
