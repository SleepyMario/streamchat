package discordnotify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SleepyMario/streamchat/internal/clientruntime"
)

type streamStatusSource interface {
	RefreshStatus(context.Context) clientruntime.State
	OpenURL(context.Context, string) (string, error)
}

type Announcement struct {
	Source streamStatusSource
}

func (a Announcement) Build(ctx context.Context) (string, error) {
	if a.Source == nil {
		return "", errors.New("stream status source is unavailable")
	}
	state := a.Source.RefreshStatus(ctx)
	type output struct {
		platform string
		name     string
		url      string
	}
	outputs := make([]output, 0, 3)
	for _, candidate := range []struct {
		platform string
		name     string
	}{
		{platform: "twitch", name: "Twitch"},
		{platform: "kick", name: "Kick"},
		{platform: "youtube", name: "YouTube"},
	} {
		status, ok := state.Streams[candidate.platform]
		if !ok || !status.Available || !status.Live {
			continue
		}
		publicURL, err := a.Source.OpenURL(ctx, candidate.platform)
		if err != nil || strings.TrimSpace(publicURL) == "" {
			continue
		}
		outputs = append(outputs, output{platform: candidate.platform, name: candidate.name, url: publicURL})
	}
	if len(outputs) == 0 {
		return "", errors.New("no live output with a public URL was detected")
	}
	names := make([]string, len(outputs))
	lines := make([]string, len(outputs))
	for i, output := range outputs {
		names[i] = output.name
		lines[i] = fmt.Sprintf("%s: %s", output.name, output.url)
	}
	return fmt.Sprintf("@everyone Sleepymario has started streaming on %s:\n%s", humanList(names), strings.Join(lines, "\n")), nil
}

func humanList(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", and " + values[len(values)-1]
	}
}
