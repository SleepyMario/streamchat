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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/gorilla/websocket"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testMessage(id string) chat.Message {
	return chat.Message{ID: id, Platform: chat.PlatformKick, Timestamp: time.Now().UTC(), AuthorDisplayName: "viewer", Text: "hello", EventType: chat.EventMessage}
}

func TestCoreStatusEndpointIsAuthenticatedAndVersionNeutral(t *testing.T) {
	server := NewServer("", "/relay", testToken, nil)
	server.Status = func() any {
		return map[string]any{"schema_version": 1, "media": map[string]any{"width": 1920, "height": 1080}}
	}
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"schema_version":1`) || !strings.Contains(response.Body.String(), `"width":1920`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control=%q", response.Header().Get("Cache-Control"))
	}
}

func TestPersistenceAndRelayUseSameMessage(t *testing.T) {
	store, err := archive.Open(filepath.Join(t.TempDir(), "streamchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay := NewServer("", "/relay", testToken, nil)
	relay.Accept = store.Store
	httpServer := httptest.NewServer(relay.Handler())
	defer httpServer.Close()
	conn, _, err := connect(t, websocketURL(httpServer.URL), testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, func() bool { return relay.hub.count() == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan chat.Message, 1)
	go func() { _ = relay.forward(ctx, messages) }()
	messages <- testMessage("persisted-and-relayed")
	if got := readMessage(t, conn); got.ID != "persisted-and-relayed" {
		t.Fatalf("unexpected message: %+v", got)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Total != 1 {
		t.Fatalf("archive stats %+v, %v", stats, err)
	}
}

func TestKickAndTwitchShareArchiveAndRelayPipeline(t *testing.T) {
	store, err := archive.Open(filepath.Join(t.TempDir(), "streamchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := NewServer("", "/relay", testToken, nil)
	server.Accept = store.Store
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	conn, _, err := connect(t, websocketURL(httpServer.URL), testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, func() bool { return server.hub.count() == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan chat.Message, 2)
	done := make(chan error, 1)
	go func() { done <- server.forward(ctx, messages) }()
	kickMessage := testMessage("kick-concurrent")
	_, parsedTwitch, err := twitch.ParseEvent([]byte(`{"metadata":{"message_id":"delivery","message_type":"notification","message_timestamp":"2026-08-19T00:00:00Z","subscription_type":"channel.chat.message","subscription_version":"1"},"payload":{"event":{"broadcaster_user_id":"channel","broadcaster_user_name":"Channel","broadcaster_user_login":"channel","chatter_user_id":"viewer-id","chatter_user_name":"viewer","chatter_user_login":"viewer","message_id":"twitch-concurrent","badges":[],"message":{"text":"hello KappaKappa","fragments":[{"type":"text","text":"hello "},{"type":"emote","text":"Kappa","emote":{"id":"25","emote_set_id":"0","owner_id":"0","format":["static"]}},{"type":"emote","text":"Kappa","emote":{"id":"25","emote_set_id":"0","owner_id":"0","format":["static"]}}]}}}}`))
	if err != nil || parsedTwitch == nil {
		t.Fatalf("parse Twitch event=%+v err=%v", parsedTwitch, err)
	}
	twitchMessage := *parsedTwitch
	go func() { messages <- kickMessage }()
	go func() { messages <- twitchMessage }()
	seen := map[string]chat.Message{}
	for range 2 {
		message := readMessage(t, conn)
		seen[message.ID] = message
	}
	if seen[kickMessage.ID].Platform != chat.PlatformKick || seen[twitchMessage.ID].Platform != chat.PlatformTwitch {
		t.Fatalf("relayed=%v", seen)
	}
	relayedTwitch := seen[twitchMessage.ID]
	if len(relayedTwitch.Emotes) != 2 || relayedTwitch.Emotes[0] != twitchMessage.Emotes[0] || relayedTwitch.Emotes[1] != twitchMessage.Emotes[1] || relayedTwitch.Emotes[0].URL != "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0" {
		t.Fatalf("Twitch emote metadata changed across archive/relay: got=%+v want=%+v", relayedTwitch.Emotes, twitchMessage.Emotes)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Total != 2 || len(stats.Platforms) != 2 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPersistenceFailureStopsRelay(t *testing.T) {
	store, err := archive.Open(filepath.Join(t.TempDir(), "streamchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	relay := NewServer("", "/relay", testToken, nil)
	relay.Accept = store.Store
	messages := make(chan chat.Message, 1)
	messages <- testMessage("must-not-relay")
	err = relay.forward(context.Background(), messages)
	if err == nil || !strings.Contains(err.Error(), "persist message before relay") {
		t.Fatalf("unexpected error: %v", err)
	}
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

func TestLocalShutdownRequiresLoopbackPostAndToken(t *testing.T) {
	shutdown := make(chan struct{}, 1)
	server := NewServer("", "/relay", testToken, nil)
	server.LocalShutdown = func() { shutdown <- struct{}{} }
	handler := server.Handler()

	tests := []struct {
		name       string
		method     string
		remoteAddr string
		token      string
		want       int
	}{
		{name: "method", method: http.MethodGet, remoteAddr: "127.0.0.1:1234", token: testToken, want: http.StatusMethodNotAllowed},
		{name: "remote", method: http.MethodPost, remoteAddr: "192.0.2.1:1234", token: testToken, want: http.StatusForbidden},
		{name: "token", method: http.MethodPost, remoteAddr: "127.0.0.1:1234", token: "wrong", want: http.StatusUnauthorized},
		{name: "accepted", method: http.MethodPost, remoteAddr: "[::1]:1234", token: testToken, want: http.StatusAccepted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://localhost/_streamchat/local-shutdown", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d", response.Code, test.want)
			}
		})
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("accepted request did not trigger shutdown")
	}
}

func TestClientReportsConnectedAndStoppedStates(t *testing.T) {
	serverRelay := NewServer("", "/relay", testToken, nil)
	httpServer := httptest.NewServer(serverRelay.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	states := make(chan string, 8)
	messages := make(chan chat.Message, 1)
	client := NewClient(websocketURL(httpServer.URL), testToken)
	client.OnState = func(state string) { states <- state }
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, messages) }()
	connected := false
	deadline := time.After(2 * time.Second)
	for !connected {
		select {
		case state := <-states:
			connected = state == "connected"
		case <-deadline:
			t.Fatal("client did not report connected")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stopped := false
	for !stopped {
		select {
		case state := <-states:
			stopped = state == "stopped"
		default:
			t.Fatal("client did not report stopped")
		}
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
	store, err := archive.Open(filepath.Join(t.TempDir(), "kick.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay.Accept = store.Store
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

	stamp := now.Format(time.RFC3339)
	sendWebhook := func(eventID string, body []byte) {
		t.Helper()
		digest := sha256.Sum256([]byte(eventID + "." + stamp + "." + string(body)))
		signature, signErr := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
		if signErr != nil {
			t.Fatal(signErr)
		}
		request, requestErr := http.NewRequest(http.MethodPost, httpServer.URL+"/webhooks/kick", bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Kick-Event-Message-Id", eventID)
		request.Header.Set("Kick-Event-Subscription-Id", "subscription-id")
		request.Header.Set("Kick-Event-Signature", base64.StdEncoding.EncodeToString(signature))
		request.Header.Set("Kick-Event-Message-Timestamp", stamp)
		request.Header.Set("Kick-Event-Type", "chat.message.sent")
		request.Header.Set("Kick-Event-Version", "1")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("webhook status %d", response.StatusCode)
		}
	}

	humanBody := []byte(`{"message_id":"kick-message","broadcaster":{"user_id":1,"username":"channel","channel_slug":"chan"},"sender":{"user_id":2,"username":"viewer"},"content":"relayed","created_at":"2026-01-01T00:00:00Z"}`)
	sendWebhook("human-event-id", humanBody)
	human := readMessage(t, conn)
	if human.ID != "kick-message" || human.Platform != chat.PlatformKick || human.AuthorDisplayName != "viewer" || human.Text != "relayed" {
		t.Fatalf("unexpected relayed human message: %+v", human)
	}

	botBody := []byte(`{"message_id":"kick-bot-message","broadcaster":{"user_id":1,"username":"channel","channel_slug":"chan"},"sender":{"user_id":22,"username":"BotRix","identity":{"username_color":"#53fc18","badges":[{"text":"Bot","type":"bot","count":1},{"text":"Moderator","type":"moderator","count":1}]}},"content":"Automated relay","created_at":"2026-01-01T00:00:00Z"}`)
	sendWebhook("bot-event-id", botBody)
	bot := readMessage(t, conn)
	if bot.ID != "kick-bot-message" || bot.Platform != chat.PlatformKick || bot.AuthorDisplayName != "BotRix" || bot.Text != "Automated relay" {
		t.Fatalf("unexpected relayed bot message: %+v", bot)
	}
	if len(bot.Badges) != 2 || bot.Badges[0].Type != "bot" || !bot.Roles.Has(chat.RoleModerator) {
		t.Fatalf("bot metadata did not survive archive/relay: %+v", bot)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Total != 2 || len(stats.Platforms) != 1 || stats.Platforms[0].Platform != "kick" || stats.Platforms[0].Count != 2 {
		t.Fatalf("Kick was not persisted: %+v, %v", stats, err)
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
