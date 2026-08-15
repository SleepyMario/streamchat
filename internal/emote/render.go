package emote

import (
	"cmp"
	"slices"
	"strings"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/mattn/go-runewidth"
)

const inlineWidth = 3

type Resolver func(chat.Platform, chat.Emote) (string, bool)

type InlineImage struct {
	Path   string
	Column int
	Width  int
}

type Line struct {
	Text          string
	GraphicalText string
	Images        []InlineImage
}

// FormatText replaces structured emote ranges with readable text or image
// placeholders. Provider-specific URL discovery stays in the adapters.
func FormatText(platform chat.Platform, text string, structured []chat.Emote, sanitize func(string) string, resolve Resolver) Line {
	if sanitize == nil {
		sanitize = func(value string) string { return value }
	}
	emotes := append([]chat.Emote(nil), structured...)
	slices.SortStableFunc(emotes, func(a, b chat.Emote) int {
		if order := cmp.Compare(a.Start, b.Start); order != 0 {
			return order
		}
		return cmp.Compare(a.End, b.End)
	})
	runes := []rune(text)
	cursor := 0
	result := Line{}
	var readable, graphical strings.Builder
	appendText := func(value string) {
		value = sanitize(value)
		readable.WriteString(value)
		graphical.WriteString(value)
	}
	for _, item := range emotes {
		if item.Start < cursor || item.Start < 0 || item.End < item.Start || item.End >= len(runes) {
			continue
		}
		appendText(string(runes[cursor:item.Start]))
		if path, ok := resolveImage(resolve, platform, item); ok {
			result.Images = append(result.Images, InlineImage{Path: path, Column: runewidth.StringWidth(graphical.String()), Width: inlineWidth})
			readable.WriteString(fallback(item, sanitize))
			graphical.WriteString(strings.Repeat(" ", inlineWidth))
		} else {
			appendText(fallback(item, sanitize))
		}
		cursor = item.End + 1
	}
	appendText(string(runes[cursor:]))
	result.Text = readable.String()
	if len(result.Images) > 0 {
		result.GraphicalText = graphical.String()
	}
	return result
}

func resolveImage(resolve Resolver, platform chat.Platform, item chat.Emote) (string, bool) {
	if resolve == nil || item.URL == "" {
		return "", false
	}
	path, ok := resolve(platform, item)
	return path, ok && path != ""
}

func fallback(item chat.Emote, sanitize func(string) string) string {
	name := strings.Trim(sanitize(item.Name), " :")
	if name == "" && item.ID != "" {
		name = "emote-" + sanitize(item.ID)
	}
	if name == "" {
		name = "emote"
	}
	return ":" + name + ":"
}
