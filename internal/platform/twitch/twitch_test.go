package twitch

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

const notification = `{"metadata":{"message_id":"delivery-1","message_type":"notification","message_timestamp":"2026-01-01T00:00:00.123Z","subscription_type":"channel.chat.message","subscription_version":"1"},"payload":{"subscription":{"status":"enabled"},"event":{"broadcaster_user_id":"100","broadcaster_user_name":"Channel","broadcaster_user_login":"channel","chatter_user_id":"200","chatter_user_name":"Viewer","chatter_user_login":"viewer","message_id":"message-1","color":"#123456","badges":[{"set_id":"moderator","id":"1","info":""}],"message":{"text":"Hi Kappa","fragments":[{"type":"text","text":"Hi "},{"type":"emote","text":"Kappa","emote":{"id":"25","owner_id":"0","format":["static"]}}]}}}}`

func TestParseEventNormalizesMessageBadgesAndEmotes(t *testing.T) {
	v, m, err := ParseEvent([]byte(notification))
	if err != nil {
		t.Fatal(err)
	}
	if v.Metadata.MessageID != "delivery-1" || m == nil || m.ID != "message-1" || m.AuthorDisplayName != "Viewer" || len(m.Badges) != 1 || len(m.Emotes) != 1 || m.Emotes[0].ID != "25" {
		t.Fatalf("%+v %+v", v, m)
	}
}

func TestValidateRejectsInvalidCredentialWithoutLeakingIt(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer s.Close()
	a := API{HTTP: s.Client(), OAuthBaseURL: s.URL, ClientID: "client", AccessToken: "super-secret-token"}
	_, err := a.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("invalid token accepted")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatal("token leaked")
	}
}

type fakeWS struct {
	messages [][]byte
	i        int
}

func (f *fakeWS) ReadMessage() (int, []byte, error) {
	if f.i >= len(f.messages) {
		return 0, nil, errors.New("done")
	}
	b := f.messages[f.i]
	f.i++
	return 1, b, nil
}
func (f *fakeWS) Close() error                    { return nil }
func (f *fakeWS) SetReadDeadline(time.Time) error { return nil }

func TestReconnectAndDuplicateDelivery(t *testing.T) {
	var subscribed int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eventsub/subscriptions" {
			t.Fatalf("path %s", r.URL.Path)
		}
		subscribed++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer s.Close()
	a := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "token"}
	c := New(a, "ws://initial", "channel", "reader")
	welcome := []byte(`{"metadata":{"message_id":"welcome","message_type":"session_welcome","message_timestamp":"2026-01-01T00:00:00Z"},"payload":{"session":{"id":"session","keepalive_timeout_seconds":10}}}`)
	reconnect := []byte(`{"metadata":{"message_id":"reconnect","message_type":"session_reconnect","message_timestamp":"2026-01-01T00:00:01Z"},"payload":{"session":{"reconnect_url":"wss://new.example/ws"}}}`)
	f := &fakeWS{messages: [][]byte{welcome, []byte(notification), []byte(notification), reconnect}}
	out := make(chan chat.Message, 4)
	next, err := c.runSocket(context.Background(), f, "100", false, out)
	if err != nil {
		t.Fatal(err)
	}
	if next != "wss://new.example/ws" || subscribed != 1 || len(out) != 1 {
		t.Fatalf("next=%q subscribed=%d messages=%d", next, subscribed, len(out))
	}
}

func TestParseChannelNameAndURL(t *testing.T) {
	for _, in := range []string{"Some_Channel", "https://www.twitch.tv/Some_Channel"} {
		got, err := ParseChannel(in)
		if err != nil || got != "some_channel" {
			t.Fatalf("%q => %q %v", in, got, err)
		}
	}
	if _, err := ParseChannel("https://example.com/nope"); err == nil {
		t.Fatal("accepted other host")
	}
}

func TestSubscriptionRefreshesExpiredToken(t *testing.T) {
	requests := 0
	updated := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/eventsub/subscriptions":
			requests++
			if requests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatalf("authorization %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusAccepted)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Fatal("missing refresh token")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":["user:read:chat"]}`))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer s.Close()
	a := API{HTTP: s.Client(), APIBaseURL: s.URL, OAuthBaseURL: s.URL, ClientID: "client", ClientSecret: "secret", AccessToken: "old-access", RefreshToken: "old-refresh", OnToken: func(tok Token) error { updated = tok.RefreshToken == "new-refresh"; return nil }}
	if err := a.Subscribe(context.Background(), "session", "broadcaster", "reader"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || !updated || a.RefreshToken != "new-refresh" {
		t.Fatalf("requests=%d updated=%v refresh=%q", requests, updated, a.RefreshToken)
	}
}
