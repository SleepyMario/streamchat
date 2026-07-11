package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedenceAndRedaction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"youtube":{"video_id":"file"},"queue_size":3}`), 0600)
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	ApplyEnv(&c, func(k string) string {
		if k == "STREAMCHAT_YOUTUBE_VIDEO_ID" {
			return "env"
		}
		return ""
	})
	if c.YouTube.VideoID != "env" || c.QueueSize != 3 {
		t.Fatalf("%+v", c)
	}
	if Redact("supersecret") == "supersecret" {
		t.Fatal("not redacted")
	}
}
func TestInvalid(t *testing.T) {
	c := Defaults()
	c.QueueSize = 0
	c.DuplicateCapacity = 0
	applyDefaults(&c)
	c.QueueSize = -1
	if c.Validate("check") == nil {
		t.Fatal("expected invalid")
	}
}
