package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/outbound"
)

type recordingURLLauncher struct {
	urls []string
	err  error
}

func (l *recordingURLLauncher) Open(publicURL string) error {
	l.urls = append(l.urls, publicURL)
	return l.err
}

func TestOpenKickUsesPublicChannelURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/channels" || r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("request=%s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"data":[{"slug":"sleepymario","stream_title":"Live"}]}`)
	}))
	defer server.Close()
	cfg := config.Config{Kick: config.Kick{AccessToken: "access-token", BroadcasterID: "123", APIBaseURL: server.URL}}
	launcher := &recordingURLLauncher{}
	controller := openController{config: cfg, kick: &kickOutboundSender{config: cfg.Kick, http: server.Client()}, launcher: launcher}
	result, err := controller.Open(context.Background(), "kick")
	if err != nil || result != "Opened Kick externally." || !reflect.DeepEqual(launcher.urls, []string{"https://kick.com/sleepymario"}) {
		t.Fatalf("result=%q error=%v urls=%v", result, err, launcher.urls)
	}
}

func TestOpenConfiguredYouTubeAndTwitchURLs(t *testing.T) {
	for _, test := range []struct {
		platform string
		config   config.Config
		wantURL  string
	}{
		{platform: "youtube", config: config.Config{YouTube: config.YouTube{VideoID: "video-id"}}, wantURL: "https://www.youtube.com/watch?v=video-id"},
		{platform: "twitch", config: config.Config{Twitch: config.Twitch{Channel: "Some_Channel"}}, wantURL: "https://www.twitch.tv/some_channel"},
	} {
		t.Run(test.platform, func(t *testing.T) {
			launcher := &recordingURLLauncher{}
			controller := openController{config: test.config, launcher: launcher}
			if _, err := controller.Open(context.Background(), test.platform); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(launcher.urls, []string{test.wantURL}) {
				t.Fatalf("urls=%v", launcher.urls)
			}
		})
	}
}

func TestOpenUsageUnsupportedAndUnconfigured(t *testing.T) {
	for _, test := range []struct {
		name     string
		argument string
		wantText string
		wantErr  string
	}{
		{name: "missing platform", wantText: "Usage: /open PLATFORM (kick, youtube, twitch)"},
		{name: "extra argument", argument: "kick extra", wantText: "Usage: /open PLATFORM (kick, youtube, twitch)"},
		{name: "unsupported", argument: "foo", wantErr: "unsupported platform"},
		{name: "youtube unconfigured", argument: "youtube", wantErr: "not currently available/configured"},
		{name: "twitch unconfigured", argument: "twitch", wantErr: "not currently available/configured"},
		{name: "kick unconfigured", argument: "kick", wantErr: "not currently available/configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			launcher := &recordingURLLauncher{}
			result, err := (openController{launcher: launcher}).Open(context.Background(), test.argument)
			if result != test.wantText || (test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr))) {
				t.Fatalf("result=%q error=%v", result, err)
			}
			if len(launcher.urls) != 0 {
				t.Fatalf("unexpected launch: %v", launcher.urls)
			}
		})
	}
}

func TestOpenLauncherFailureDoesNotLeakOrChangeTarget(t *testing.T) {
	sender := &recordingOutboundSender{}
	targets := outbound.NewTargets(outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: sender})
	if _, err := targets.Process(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	launcher := &recordingURLLauncher{err: errors.New("could not start mpv")}
	cfg := config.Config{Twitch: config.Twitch{Channel: "sleepymario"}}
	controller := openController{config: cfg, launcher: launcher}
	targets.RegisterControl("open", outbound.ControlFunc(controller.Open))
	if _, err := targets.Process(context.Background(), "/open twitch"); err == nil || !strings.Contains(err.Error(), "could not start mpv") {
		t.Fatalf("error=%v", err)
	}
	if targets.Selected() != "kick" || len(sender.messages) != 0 {
		t.Fatalf("selected=%q messages=%v", targets.Selected(), sender.messages)
	}
}
