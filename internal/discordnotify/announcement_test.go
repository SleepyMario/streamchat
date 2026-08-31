package discordnotify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SleepyMario/streamchat/internal/clientruntime"
)

type fakeStatusSource struct {
	state clientruntime.State
	urls  map[string]string
}

func (s fakeStatusSource) RefreshStatus(context.Context) clientruntime.State { return s.state }

func (s fakeStatusSource) OpenURL(_ context.Context, platform string) (string, error) {
	if publicURL := s.urls[platform]; publicURL != "" {
		return publicURL, nil
	}
	return "", errors.New("unavailable")
}

func TestAnnouncementIncludesEveryConfirmedLiveOutputOnce(t *testing.T) {
	source := fakeStatusSource{
		state: clientruntime.State{Streams: map[string]clientruntime.StreamStatus{
			"twitch":  {Platform: "twitch", Available: true, Live: true},
			"kick":    {Platform: "kick", Available: true, Live: true},
			"youtube": {Platform: "youtube", Available: true, Live: true},
		}},
		urls: map[string]string{
			"twitch":  "https://www.twitch.tv/sleepymario",
			"kick":    "https://kick.com/sleepymario",
			"youtube": "https://www.youtube.com/watch?v=video-id",
		},
	}
	message, err := (Announcement{Source: source}).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "@everyone Sleepymario has started streaming on Twitch, Kick, and YouTube:\n" +
		"Twitch: https://www.twitch.tv/sleepymario\n" +
		"Kick: https://kick.com/sleepymario\n" +
		"YouTube: https://www.youtube.com/watch?v=video-id"
	if message != want {
		t.Fatalf("message:\n%s\nwant:\n%s", message, want)
	}
	if strings.Count(message, "@everyone") != 1 {
		t.Fatalf("@everyone count=%d, want 1", strings.Count(message, "@everyone"))
	}
}

func TestAnnouncementOmitsOfflineAndUnresolvableOutputs(t *testing.T) {
	source := fakeStatusSource{
		state: clientruntime.State{Streams: map[string]clientruntime.StreamStatus{
			"twitch":  {Platform: "twitch", Available: true, Live: true},
			"kick":    {Platform: "kick", Available: true, Live: false},
			"youtube": {Platform: "youtube", Available: true, Live: true},
		}},
		urls: map[string]string{"twitch": "https://www.twitch.tv/sleepymario"},
	}
	message, err := (Announcement{Source: source}).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if message != "@everyone Sleepymario has started streaming on Twitch:\nTwitch: https://www.twitch.tv/sleepymario" {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestAnnouncementWaitsWhenNoLivePublicOutputExists(t *testing.T) {
	_, err := (Announcement{Source: fakeStatusSource{state: clientruntime.State{Streams: map[string]clientruntime.StreamStatus{}}}}).Build(context.Background())
	if err == nil {
		t.Fatal("expected unavailable-output error")
	}
}
