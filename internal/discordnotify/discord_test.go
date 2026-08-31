package discordnotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsAuthenticatedMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/channels/123456789/messages" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bot private-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		var payload struct {
			Content         string          `json:"content"`
			AllowedMentions allowedMentions `json:"allowed_mentions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Content != "SleepyMario is live now!" {
			t.Fatalf("unexpected content: %q", payload.Content)
		}
		if len(payload.AllowedMentions.Parse) != 1 || payload.AllowedMentions.Parse[0] != "everyone" || len(payload.AllowedMentions.Users) != 0 || len(payload.AllowedMentions.Roles) != 0 {
			t.Fatalf("unexpected allowed mentions: %+v", payload.AllowedMentions)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	client, err := newClient("123456789", "private-token", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Send(context.Background(), "SleepyMario is live now!"); err != nil {
		t.Fatal(err)
	}
}

func TestClientDoesNotExposeDiscordErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive upstream detail", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := newClient("123", "private-token", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.Send(context.Background(), "test")
	if err == nil || err.Error() != "Discord rejected the message with HTTP 401" {
		t.Fatalf("unexpected error: %v", err)
	}
}
