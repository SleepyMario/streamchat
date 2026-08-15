package setup

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SleepyMario/streamchat/internal/config"
)

func TestSelectionParsing(t *testing.T) {
	got, err := ParseSelection("1, twitch 1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"youtube", "twitch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if _, err = ParseSelection(""); !errors.Is(err, ErrCancelled) {
		t.Fatalf("%v", err)
	}
	if _, err = ParseSelection("instagram"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
}

func TestCancelledSetupDoesNotCreateOrChangeConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	w := New(strings.NewReader("\n"), &out, p)
	if err := w.Run(context.Background(), nil); !errors.Is(err, ErrCancelled) {
		t.Fatalf("%v", err)
	}
	if _, err := config.Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestYouTubeSetupPreservesOtherPlatformsAndExistingSecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.YouTube.APIKey = "old-key"
	c.Kick.ClientID = "kick-existing"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := New(strings.NewReader("y\n"), &out, p)
	if err := w.Run(context.Background(), []string{"youtube"}); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.YouTube.APIKey != "old-key" || got.Kick.ClientID != "kick-existing" {
		t.Fatalf("existing values overwritten: %+v", got)
	}
	if strings.Contains(out.String(), "old-key") {
		t.Fatal("secret printed")
	}
}

func TestSecretInputTrimsSurroundingWhitespace(t *testing.T) {
	var out bytes.Buffer
	w := New(strings.NewReader("  pasted-secret\r\n"), &out, "")
	got, err := w.value("Secret", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "pasted-secret" {
		t.Fatalf("secret input was not normalized: %q", got)
	}
}

func TestKickInstructionsExplainPortalWebhookSourceOfTruth(t *testing.T) {
	c := config.Defaults()
	text := kickInstructions(c)
	for _, want := range []string{
		"http://localhost:8789/oauth/kick/callback",
		"Enter that same portal webhook URL below as kick.webhook_url",
		"does not send it in the Kick event-subscription request",
		"Changing the JSON value alone does not change Kick's webhook destination",
		"streamchat kick subscribe",
		"chat:write",
		"channel:write",
		"does not request moderation, stream-key, or rewards access",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kick instructions missing %q:\n%s", want, text)
		}
	}
}
