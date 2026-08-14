package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestSaveCreatesPrivateFileAndRoundTripsMergedConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "streamchat", "config.json")
	c := Defaults()
	c.YouTube.APIKey = "youtube-secret"
	c.Kick.ClientID = "keep-me"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("config mode %o", st.Mode().Perm())
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c2.Twitch.ClientID = "new-platform"
	if err = Save(p, c2); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.YouTube.APIKey != "youtube-secret" || got.Kick.ClientID != "keep-me" || got.Twitch.ClientID != "new-platform" {
		t.Fatalf("merge lost values: %+v", got)
	}
}

func TestRedactedJSONContainsNoSecrets(t *testing.T) {
	c := Defaults()
	c.YouTube.APIKey = "YT-VERY-SECRET"
	c.Kick.ClientSecret = "KICK-VERY-SECRET"
	c.Kick.AccessToken = "KICK-TOKEN"
	c.Twitch.RefreshToken = "TWITCH-REFRESH"
	b, err := RedactedJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"YT-VERY-SECRET", "KICK-VERY-SECRET", "KICK-TOKEN", "TWITCH-REFRESH"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	var v map[string]any
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFileModeRejectsGroupReadableSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CheckFileMode(p); err == nil {
		t.Fatal("insecure mode accepted")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckFileMode(p); err != nil {
		t.Fatal(err)
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
