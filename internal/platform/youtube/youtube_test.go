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
