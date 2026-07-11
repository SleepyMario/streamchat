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
