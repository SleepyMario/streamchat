package render

import (
	"bytes"
	"github.com/SleepyMario/streamchat/internal/chat"
	"strings"
	"testing"
	"time"
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

func TestRoleBadgeRendering(t *testing.T) {
	tests := []struct {
		name   string
		badges []chat.Badge
		want   string
	}{
		{
			name:   "moderator",
			badges: []chat.Badge{{Type: "moderator", Text: "Moderator"}},
			want:   "[KICK] user [MOD]          hello\n",
		},
		{
			name:   "broadcaster omitted",
			badges: []chat.Badge{{Type: "broadcaster", Text: "Broadcaster"}, {Type: "subscriber", Text: "Subscriber"}},
			want:   "[KICK] user [SUBSCRIBER]   hello\n",
		},
		{
			name:   "verified channel omitted",
			badges: []chat.Badge{{Type: "verified", Text: "Verified Channel"}, {Type: "subscriber", Text: "Subscriber"}},
			want:   "[KICK] user [SUBSCRIBER]   hello\n",
		},
		{
			name: "moderator and verified channel",
			badges: []chat.Badge{
				{Type: "moderator", Text: "Moderator"},
				{Type: "verified", Text: "Verified Channel"},
			},
			want: "[KICK] user [MOD]          hello\n",
		},
		{
			name:   "broadcaster only",
			badges: []chat.Badge{{Type: "broadcaster", Text: "Broadcaster"}},
			want:   "[KICK] user                hello\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			m := chat.Message{
				Platform:          chat.PlatformKick,
				AuthorDisplayName: "user",
				Badges:            tt.badges,
				Text:              "hello",
			}
			if err := New(&b, Options{AuthorWidth: 4}).Render(m); err != nil {
				t.Fatal(err)
			}
			if got := b.String(); got != tt.want {
				t.Fatalf("Render() = %q, want %q", got, tt.want)
			}
		})
	}
}
