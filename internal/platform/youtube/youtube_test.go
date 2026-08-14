package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
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

func TestServerDiscoveryStreamReconnectAndTokenRefresh(t *testing.T) {
	var streamCalls int
	var tokenPersisted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if strings.Contains(r.URL.RawQuery, "secret") || strings.Contains(r.URL.RawQuery, "refresh-value") {
				t.Fatal("OAuth secret leaked in URL")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-value" {
				t.Fatalf("bad refresh form: %v", r.Form)
			}
			w.Write([]byte(`{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer"}`))
		case "/liveBroadcasts":
			if r.Header.Get("Authorization") != "Bearer fresh-access" || r.URL.Query().Get("broadcastStatus") != "active" {
				t.Fatalf("bad discovery request: %s %v", r.Header.Get("Authorization"), r.URL.Query())
			}
			w.Write([]byte(`{"items":[{"id":"broadcast-1","snippet":{"title":"Live","liveChatId":"chat-1"}}]}`))
		case "/liveChat/messages/stream":
			streamCalls++
			if streamCalls > 1 && r.URL.Query().Get("pageToken") != "resume-token" {
				t.Fatalf("stream reconnect lost page token: %s", r.URL.String())
			}
			switch streamCalls {
			case 1:
				w.Write([]byte(`{"nextPageToken":"resume-token","items":[{"id":"yt-1","snippet":{"type":"textMessageEvent","liveChatId":"chat-1","publishedAt":"2026-08-14T01:02:03Z","displayMessage":"hello"},"authorDetails":{"channelId":"user-1","displayName":"Ada"}}]}`))
			case 2:
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":{"errors":[{"reason":"backendError"}]}}`))
			default:
				w.Write([]byte(`{"nextPageToken":"resume-token","offlineAt":"2026-08-14T01:03:00Z","items":[]}`))
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := New(server.Client(), server.URL, "", "expired-access", "")
	c.ClientID = "client-id"
	c.ClientSecret = "client-secret"
	c.RefreshToken = "refresh-value"
	c.TokenExpiry = time.Now().Add(-time.Minute)
	c.TokenURL = server.URL + "/token"
	c.RetryDelay = 10 * time.Millisecond
	c.OnToken = func(tok Token) error {
		tokenPersisted = tok.AccessToken == "fresh-access" && tok.RefreshToken == "refresh-value"
		return nil
	}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		if d == c.RetryDelay {
			cancel()
			return context.Canceled
		}
		return nil
	}
	out := make(chan chat.Message, 1)
	if err := c.RunServer(ctx, out); err != nil {
		t.Fatal(err)
	}
	if !tokenPersisted || streamCalls != 3 {
		t.Fatalf("persisted=%v stream calls=%d", tokenPersisted, streamCalls)
	}
	got := <-out
	if got.ID != "yt-1" || got.ChannelID != "broadcast-1" || got.Text != "hello" {
		t.Fatalf("unexpected message: %+v", got)
	}
}

func TestOAuthErrorRedactsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()
	client := OAuthClient{HTTP: server.Client(), TokenURL: server.URL, ClientID: "client-secret-id", ClientSecret: "client-secret-value"}
	_, err := client.Refresh(context.Background(), "refresh-secret-value")
	if err == nil {
		t.Fatal("expected OAuth rejection")
	}
	for _, secret := range []string{"client-secret-id", "client-secret-value", "refresh-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked in error: %v", err)
		}
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
