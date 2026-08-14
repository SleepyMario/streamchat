package youtube

import (
	"context"
	"errors"
	"github.com/SleepyMario/streamchat/internal/chat"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryPollingTokenAndEnded(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		if strings.Contains(r.URL.Path, "/videos") {
			w.Write([]byte(`{"items":[{"id":"vid","liveStreamingDetails":{"activeLiveChatId":"live"}}]}`))
			return
		}
		if r.URL.Query().Get("pageToken") == "next" {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":{"errors":[{"reason":"liveChatEnded"}]}}`))
			return
		}
		w.Write([]byte(`{"nextPageToken":"next","pollingIntervalMillis":25,"items":[{"id":"m1","snippet":{"type":"textMessageEvent","publishedAt":"2026-01-01T00:00:00Z","displayMessage":"hello"},"authorDetails":{"channelId":"u","displayName":"Ada"}}]}`))
	}))
	defer s.Close()
	c := New(s.Client(), s.URL, "key", "", "vid")
	var waited time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { waited = d; return nil }
	out := make(chan chat.Message, 2)
	if e := c.Run(context.Background(), out); e != nil {
		t.Fatal(e)
	}
	m := <-out
	if m.Text != "hello" || m.EventType != chat.EventMessage {
		t.Fatalf("%+v", m)
	}
	if waited != 25*time.Millisecond {
		t.Fatalf("wait %v", waited)
	}
	if !strings.Contains(paths[len(paths)-1], "pageToken=next") {
		t.Fatalf("paths %v", paths)
	}
}
func TestRetryClassification(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"errors":[{"reason":"forbidden"}]}}`))
	}))
	defer s.Close()
	c := New(s.Client(), s.URL, "x", "", "v")
	_, _, e := c.Discover(context.Background())
	var ae *chat.AdapterError
	if !errors.As(e, &ae) || ae.Kind != chat.Authentication {
		t.Fatalf("%v", e)
	}
}

func TestParseVideoID(t *testing.T) {
	want := "abc123_DEF-"
	for _, in := range []string{want, "https://www.youtube.com/watch?v=" + want + "&feature=share", "https://youtu.be/" + want, "https://www.youtube.com/live/" + want + "?si=x"} {
		got, err := ParseVideoID(in)
		if err != nil || got != want {
			t.Errorf("%q => %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "https://example.com/watch?v=x", "https://youtube.com/channel/name", "bad/id"} {
		if _, err := ParseVideoID(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestAPIKeyUsesHeaderNotURL(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "secret-key") {
			t.Fatal("API key leaked into URL")
		}
		if r.Header.Get("X-Goog-Api-Key") != "secret-key" {
			t.Fatal("missing API key header")
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	defer s.Close()
	c := New(s.Client(), s.URL, "secret-key", "", "")
	if err := c.ValidateCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
}
