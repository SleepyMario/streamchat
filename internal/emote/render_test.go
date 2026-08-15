package emote

import (
	"testing"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func TestFormatTextFallbackMixedAndMultipleEmotes(t *testing.T) {
	text := "hello [emote:7:WAVE] + [emote:8:CLAP]"
	items := []chat.Emote{
		{ID: "7", Name: "WAVE", Start: 6, End: 19},
		{ID: "8", Name: "CLAP", Start: 23, End: 36},
	}
	line := FormatText(chat.PlatformKick, text, items, nil, nil)
	if line.Text != "hello :WAVE: + :CLAP:" || len(line.Images) != 0 {
		t.Fatalf("unexpected fallback: %+v", line)
	}
}

func TestFormatTextUsesProviderNeutralResolver(t *testing.T) {
	item := chat.Emote{ID: "7", Name: "WAVE", URL: "https://files.kick.com/emotes/7/fullsize", Start: 2, End: 15}
	line := FormatText(chat.PlatformKick, "a [emote:7:WAVE] z", []chat.Emote{item}, nil, func(platform chat.Platform, got chat.Emote) (string, bool) {
		if platform != chat.PlatformKick || got.ID != "7" {
			t.Fatalf("resolver received %q %+v", platform, got)
		}
		return "/cache/kick/7.img", true
	})
	if line.Text != "a    z" || len(line.Images) != 1 || line.Images[0].Column != 2 || line.Images[0].Width != 2 {
		t.Fatalf("unexpected image line: %+v", line)
	}
}

func TestFormatTextMissingImageFallsBackToReadableName(t *testing.T) {
	item := chat.Emote{ID: "7", Name: "ppJedi", URL: "https://example.invalid/7", Start: 0, End: 15}
	line := FormatText(chat.PlatformKick, "[emote:7:ppJedi]", []chat.Emote{item}, nil, func(chat.Platform, chat.Emote) (string, bool) { return "", false })
	if line.Text != ":ppJedi:" {
		t.Fatalf("fallback = %q", line.Text)
	}
}

func TestControllerWithoutImageBackendFallsBack(t *testing.T) {
	controller := NewController(ControllerOptions{Mode: "auto"})
	if controller.Available() {
		t.Fatal("controller reported missing backend as available")
	}
	if path, ok := controller.Resolve(chat.PlatformKick, chat.Emote{ID: "7", URL: "https://files.kick.com/emotes/7/fullsize"}); ok || path != "" {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
}
