package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func TestSanitizeAndUnicode(t *testing.T) {
	s := Sanitize("héllo\x1b[31mBAD\x1b[0m\x07\nnext")
	if s != "hélloBAD next" {
		t.Fatalf("%q", s)
	}
}
func TestNoColorOutput(t *testing.T) {
	var b bytes.Buffer
	m := chat.Message{ID: "1", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "名", Text: "ok", EventType: chat.EventMessage}
	if e := New(&b, Options{Color: false}).Render(m); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(b.String(), "\x1b") || !strings.Contains(b.String(), "名") {
		t.Fatal(b.String())
	}
}
func TestNOColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(false) {
		t.Fatal("NO_COLOR ignored")
	}
}

func TestIdentityRendering(t *testing.T) {
	tests := []struct {
		name        string
		author      string
		badges      []chat.Badge
		authorWidth int
		want        string
	}{
		{
			name:   "normal username alignment",
			author: "SleepyMario",
			want:   "[KICK] SleepyMario       hello\n",
		},
		{
			name:   "moderator before username",
			author: "BotRix",
			badges: []chat.Badge{{Type: "moderator", Text: "Moderator"}},
			want:   "[KICK] [MOD] BotRix      hello\n",
		},
		{
			name:   "long username exceeding cap",
			author: "abcdefghijklmnopqrstuvwx",
			want:   "[KICK] abcdefghijklmnopqrstuvwx  hello\n",
		},
		{
			name:        "long moderator identity exceeding cap",
			author:      "abcdefghijklmnopq",
			authorWidth: 40,
			badges:      []chat.Badge{{Type: "moderator", Text: "Moderator"}},
			want:        "[KICK] [MOD] abcdefghijklmnopq  hello\n",
		},
		{
			name:   "other badges preserved and omitted roles remain omitted",
			author: "user",
			badges: []chat.Badge{
				{Type: "broadcaster", Text: "Broadcaster"},
				{Type: "verified", Text: "Verified Channel"},
				{Type: "subscriber", Text: "Subscriber"},
			},
			want: "[KICK] [SUBSCRIBER] user  hello\n",
		},
		{
			name:        "unicode display width alignment",
			author:      "名",
			authorWidth: 4,
			want:        "[KICK] 名    hello\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			m := chat.Message{
				Platform:          chat.PlatformKick,
				AuthorDisplayName: tt.author,
				Badges:            tt.badges,
				Text:              "hello",
			}
			if err := New(&b, Options{AuthorWidth: tt.authorWidth}).Render(m); err != nil {
				t.Fatal(err)
			}
			if got := b.String(); got != tt.want {
				t.Fatalf("Render() = %q, want %q", got, tt.want)
			}
		})
	}
}
