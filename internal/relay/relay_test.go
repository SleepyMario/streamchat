package relay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/gorilla/websocket"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testMessage(id string) chat.Message {
	return chat.Message{ID: id, Platform: chat.PlatformKick, Timestamp: time.Now().UTC(), AuthorDisplayName: "viewer", Text: "hello", EventType: chat.EventMessage}
}

func websocketURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + "/relay"
}

func connect(t *testing.T, url, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := make(http.Header)
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	return conn, response, err
}

func readMessage(t *testing.T, conn *websocket.Conn) chat.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var m chat.Message
	if err = json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func TestAuthorizedConnectionAndUnauthorizedRejection(t *testing.T) {
	relay := NewServer("", "/relay", testToken, nil)
	httpServer := httptest.NewServer(relay.Handler())
	defer httpServer.Close()
	url := websocketURL(httpServer.URL)

	conn, response, err := connect(t, url, "wrong-token")
	if conn != nil {
		conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized connection: response=%v err=%v", response, err)
	}
	response.Body.Close()

	conn, response, err = connect(t, url, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response != nil {
		response.Body.Close()
	}
	waitFor(t, func() bool { return relay.hub.count() == 1 })
	if err = relay.Broadcast(testMessage("authorized")); err != nil {
		t.Fatal(err)
	}
	if got := readMessage(t, conn); got.ID != "authorized" {
		t.Fatalf("unexpected message: %+v", got)
	}
}

func TestBroadcastReachesMultipleClients(t *testing.T) {
	relay := NewServer("", "/relay", testToken, nil)
	httpServer := httptest.NewServer(relay.Handler())
	defer httpServer.Close()
	url := websocketURL(httpServer.URL)
	first, _, err := connect(t, url, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, _, err := connect(t, url, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	waitFor(t, func() bool { return relay.hub.count() == 2 })
	if err = relay.Broadcast(testMessage("everyone")); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{first, second} {
		if got := readMessage(t, conn); got.ID != "everyone" {
			t.Fatalf("unexpected message: %+v", got)
		}
	}
}

func TestVerifiedKickWebhookRelaysNormalizedMessage(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan chat.Message, 1)
	now := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	webhook := kick.NewServer("", &privateKey.PublicKey, messages)
	webhook.Now = func() time.Time { return now }
	relay := NewServer("", "/relay", testToken, webhook.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.forward(ctx, messages) }()
	httpServer := httptest.NewServer(relay.Handler())
	defer httpServer.Close()
	conn, _, err := connect(t, websocketURL(httpServer.URL), testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, func() bool { return relay.hub.count() == 1 })

	body := []byte(`{"message_id":"kick-message","broadcaster":{"user_id":1,"username":"channel","channel_slug":"chan"},"sender":{"user_id":2,"username":"viewer"},"content":"relayed","created_at":"2026-01-01T00:00:00Z"}`)
	eventID := "event-id"
	stamp := now.Format(time.RFC3339)
	digest := sha256.Sum256([]byte(eventID + "." + stamp + "." + string(body)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/webhooks/kick", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Kick-Event-Message-Id", eventID)
	request.Header.Set("Kick-Event-Subscription-Id", "subscription-id")
	request.Header.Set("Kick-Event-Signature", base64.StdEncoding.EncodeToString(signature))
	request.Header.Set("Kick-Event-Message-Timestamp", stamp)
	request.Header.Set("Kick-Event-Type", "chat.message.sent")
	request.Header.Set("Kick-Event-Version", "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook status %d", response.StatusCode)
	}
	got := readMessage(t, conn)
	if got.ID != "kick-message" || got.Platform != chat.PlatformKick || got.Text != "relayed" {
		t.Fatalf("unexpected relayed message: %+v", got)
	}
}

func TestClientReconnectsAfterDisconnect(t *testing.T) {
	relay := NewServer("", "/relay", testToken, nil)
	httpServer := httptest.NewServer(relay.Handler())
	defer httpServer.Close()
	client := NewClient(websocketURL(httpServer.URL), testToken)
	client.RetryDelay = 10 * time.Millisecond
	var attempts atomic.Int32
	client.Dial = func(ctx context.Context, url string, header http.Header) (*websocket.Conn, *http.Response, error) {
		attempts.Add(1)
		return websocket.DefaultDialer.DialContext(ctx, url, header)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan chat.Message, 2)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, out) }()
	waitFor(t, func() bool { return attempts.Load() >= 1 && relay.hub.count() == 1 })
	relay.hub.closeAll()
	waitFor(t, func() bool { return attempts.Load() >= 2 && relay.hub.count() == 1 })
	if err := relay.Broadcast(testMessage("after-reconnect")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-out:
		if m.ID != "after-reconnect" {
			t.Fatalf("unexpected message: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnected client did not receive a message")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}
