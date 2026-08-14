package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/config"
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
	if c := run([]string{"--help"}, &out, &err); c != 0 || !strings.Contains(out.String(), "kick subscribe") {
		t.Fatal(out.String())
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
	if err := runAdapters(context.Background(), adapters, c, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[YT]") || !strings.Contains(out.String(), "[TW]") {
		t.Fatalf("%s / %s", out.String(), errw.String())
	}
}

func TestSafeErrorRedactsSecrets(t *testing.T) {
	s := safeError(errors.New("request?access_token=secret-token&client_secret=secret-client next"))
	if strings.Contains(s, "secret-token") || strings.Contains(s, "secret-client") {
		t.Fatal(s)
	}
}

func TestConfigShowRedactsEverySecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.YouTube.APIKey = "youtube-private"
	c.Kick.ClientSecret = "kick-private"
	c.Twitch.AccessToken = "twitch-private"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"config", "show", "--config", p}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, s := range []string{"youtube-private", "kick-private", "twitch-private"} {
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
