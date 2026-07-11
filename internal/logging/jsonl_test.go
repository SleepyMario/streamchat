package logging

import (
	"encoding/json"
	"github.com/SleepyMario/streamchat/internal/chat"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chat.jsonl")
	l, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	m := chat.Message{ID: "1", Platform: chat.PlatformYouTube, Timestamp: time.Now(), Text: "你好", EventType: chat.EventMessage}
	if e = l.Write(m); e != nil {
		t.Fatal(e)
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	var got chat.Message
	if e = json.Unmarshal(b, &got); e != nil || got.Text != "你好" {
		t.Fatalf("%s %v", b, e)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}
func TestOpenError(t *testing.T) {
	if _, e := Open(t.TempDir()); e == nil {
		t.Fatal("expected error")
	}
}
