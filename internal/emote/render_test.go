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
	if line.Text != "a :WAVE: z" || line.GraphicalText != "a     z" || len(line.Images) != 1 || line.Images[0].Column != 2 || line.Images[0].Width != 3 {
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

func TestTwitchAdjacentGraphicalEmotesUseUnicodeDisplayColumns(t *testing.T) {
	text := "你好KappaPogChamp!"
	items := []chat.Emote{
		{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0", Start: 2, End: 6},
		{ID: "305954156", Name: "PogChamp", URL: "https://static-cdn.jtvnw.net/emoticons/v2/305954156/static/dark/3.0", Start: 7, End: 14},
	}
	line := FormatText(chat.PlatformTwitch, text, items, nil, func(_ chat.Platform, item chat.Emote) (string, bool) {
		return "/cache/twitch/" + item.ID + ".img", true
	})
	if line.Text != text || line.GraphicalText != "你好      !" || len(line.Images) != 2 {
		t.Fatalf("line=%+v", line)
	}
	if line.Images[0].Column != 4 || line.Images[1].Column != 7 || line.Images[0].Width != 3 || line.Images[1].Width != 3 {
		t.Fatalf("images=%+v", line.Images)
	}
}

func TestGraphicalPlacementKeysStayWithEmoteOccurrencesAsCacheHitsChange(t *testing.T) {
	text := "KappaPogChampKappa"
	items := []chat.Emote{
		{ID: "25", Name: "Kappa", URL: "https://example.invalid/25", Start: 0, End: 4},
		{ID: "305954156", Name: "PogChamp", URL: "https://example.invalid/305954156", Start: 5, End: 12},
		{ID: "25", Name: "Kappa", URL: "https://example.invalid/25", Start: 13, End: 17},
	}
	available := map[string]bool{"305954156": true}
	resolve := func(_ chat.Platform, item chat.Emote) (string, bool) {
		return "/cache/twitch/" + item.ID + ".img", available[item.ID]
	}
	partial := FormatText(chat.PlatformTwitch, text, items, nil, resolve)
	if len(partial.Images) != 1 || partial.Images[0].PlacementKey == "" {
		t.Fatalf("partial=%+v", partial)
	}
	pogKey := partial.Images[0].PlacementKey
	available["25"] = true
	complete := FormatText(chat.PlatformTwitch, text, items, nil, resolve)
	if len(complete.Images) != 3 || complete.Images[1].PlacementKey != pogKey {
		t.Fatalf("complete=%+v pogKey=%q", complete, pogKey)
	}
	if complete.Images[0].PlacementKey == complete.Images[2].PlacementKey {
		t.Fatalf("repeated emote occurrences share placement key: %+v", complete.Images)
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
