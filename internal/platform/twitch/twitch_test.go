package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/emote"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	if v.Metadata.MessageID != "delivery-1" || m == nil || m.ID != "message-1" || m.AuthorDisplayName != "Viewer" || len(m.Badges) != 1 || m.Badges[0].Text != "1" || len(m.Emotes) != 1 || m.Emotes[0].ID != "25" || m.Emotes[0].Start != 3 || m.Emotes[0].End != 7 || m.Emotes[0].URL != "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0" {
		t.Fatalf("%+v %+v", v, m)
	}
	if m.ChannelID != "100" || m.ChannelDisplayName != "Channel" || m.AuthorID != "200" || m.Text != "Hi Kappa" || m.SafePlatformMetadata["twitch_broadcaster_login"] != "channel" {
		t.Fatalf("message fields=%+v", m)
	}
}

func twitchFragmentNotification(text string, fragments []map[string]any) []byte {
	payload := map[string]any{
		"metadata": map[string]any{"message_id": "delivery-emotes", "message_type": "notification", "message_timestamp": "2026-01-01T00:00:00Z", "subscription_type": EventType, "subscription_version": "1"},
		"payload": map[string]any{"subscription": map[string]any{"status": "enabled"}, "event": map[string]any{
			"broadcaster_user_id": "100", "broadcaster_user_name": "Channel", "broadcaster_user_login": "channel",
			"chatter_user_id": "200", "chatter_user_name": "Viewer", "chatter_user_login": "viewer", "message_id": "message-emotes",
			"message": map[string]any{"text": text, "fragments": fragments},
		}},
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func twitchTextFragment(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func twitchEmoteFragment(name, id string, formats ...string) map[string]any {
	return map[string]any{"type": "emote", "text": name, "emote": map[string]any{"id": id, "emote_set_id": "0", "owner_id": "0", "format": formats}}
}

func TestTwitchStructuredEmoteRangesUseInclusiveRunePositions(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		fragments []map[string]any
		want      []chat.Emote
	}{
		{"single", "Kappa", []map[string]any{twitchEmoteFragment("Kappa", "25", "static")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 0, End: 4}}},
		{"multiple", "hello Kappa world PogChamp !", []map[string]any{twitchTextFragment("hello "), twitchEmoteFragment("Kappa", "25", "static"), twitchTextFragment(" world "), twitchEmoteFragment("PogChamp", "305954156", "static"), twitchTextFragment(" !")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 6, End: 10}, {ID: "305954156", Name: "PogChamp", Start: 18, End: 25}}},
		{"adjacent", "KappaPogChamp", []map[string]any{twitchEmoteFragment("Kappa", "25", "static"), twitchEmoteFragment("PogChamp", "305954156", "static")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 0, End: 4}, {ID: "305954156", Name: "PogChamp", Start: 5, End: 12}}},
		{"repeated", "Kappa Kappa", []map[string]any{twitchEmoteFragment("Kappa", "25", "static"), twitchTextFragment(" "), twitchEmoteFragment("Kappa", "25", "static")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 0, End: 4}, {ID: "25", Name: "Kappa", Start: 6, End: 10}}},
		{"CJK", "你好 Kappa 世界", []map[string]any{twitchTextFragment("你好 "), twitchEmoteFragment("Kappa", "25", "static"), twitchTextFragment(" 世界")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 3, End: 7}}},
		{"emoji", "🙂 Kappa 🚀", []map[string]any{twitchTextFragment("🙂 "), twitchEmoteFragment("Kappa", "25", "static"), twitchTextFragment(" 🚀")}, []chat.Emote{{ID: "25", Name: "Kappa", Start: 2, End: 6}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, message, err := ParseEvent(twitchFragmentNotification(test.text, test.fragments))
			if err != nil {
				t.Fatal(err)
			}
			if len(message.Emotes) != len(test.want) {
				t.Fatalf("emotes=%+v want=%+v", message.Emotes, test.want)
			}
			for index, want := range test.want {
				got := message.Emotes[index]
				if got.ID != want.ID || got.Name != want.Name || got.Start != want.Start || got.End != want.End || got.End-got.Start+1 != len([]rune(got.Name)) || got.URL == "" {
					t.Fatalf("emote[%d]=%+v want=%+v", index, got, want)
				}
			}
			line := emote.FormatText(chat.PlatformTwitch, message.Text, message.Emotes, nil, nil)
			if line.Text != test.text || line.GraphicalText != "" || len(line.Images) != 0 {
				t.Fatalf("fallback=%+v", line)
			}
		})
	}
}

func TestThirdPartyExtensionNamesRemainReadableEventSubText(t *testing.T) {
	text := "D: tehPoleCat FeelsSnowMan"
	_, message, err := ParseEvent(twitchFragmentNotification(text, []map[string]any{twitchTextFragment(text)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Emotes) != 0 {
		t.Fatalf("third-party text was invented as Twitch emotes: %+v", message.Emotes)
	}
	resolverCalled := false
	line := emote.FormatText(chat.PlatformTwitch, message.Text, message.Emotes, nil, func(chat.Platform, chat.Emote) (string, bool) {
		resolverCalled = true
		return "", false
	})
	if resolverCalled || line.Text != text || line.GraphicalText != "" || len(line.Images) != 0 {
		t.Fatalf("line=%+v resolverCalled=%t", line, resolverCalled)
	}
}

func TestTwitchEmoteURLUsesDocumentedTemplatePolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		id      string
		formats []string
		want    string
	}{
		{"numeric static", "25", []string{"static"}, "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0"},
		{"string static", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49", []string{"static"}, "https://static-cdn.jtvnw.net/emoticons/v2/emotesv2_4c3b4ed516de493bbcd2df2f5d450f49/static/dark/3.0"},
		{"animated preferred", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49", []string{"static", "animated"}, "https://static-cdn.jtvnw.net/emoticons/v2/emotesv2_4c3b4ed516de493bbcd2df2f5d450f49/animated/dark/3.0"},
		{"animated only", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49", []string{"animated"}, "https://static-cdn.jtvnw.net/emoticons/v2/emotesv2_4c3b4ed516de493bbcd2df2f5d450f49/animated/dark/3.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := TwitchEmoteURL(test.id, test.formats); got != test.want {
				t.Fatalf("URL=%q want=%q", got, test.want)
			}
		})
	}
	for _, invalid := range []struct {
		id      string
		formats []string
	}{{"", []string{"static"}}, {"../25", []string{"static"}}, {"25/other", []string{"static"}}, {"25%2fother", []string{"static"}}, {"表情", []string{"static"}}, {strings.Repeat("a", 129), []string{"static"}}, {"25", nil}, {"25", []string{"webp"}}} {
		if got := TwitchEmoteURL(invalid.id, invalid.formats); got != "" {
			t.Fatalf("unsafe metadata produced URL %q", got)
		}
	}
}

func TestInvalidTwitchEmoteMetadataKeepsReadableProviderName(t *testing.T) {
	fragments := []map[string]any{twitchTextFragment("hello "), twitchEmoteFragment("Kappa", "../unsafe", "static")}
	_, message, err := ParseEvent(twitchFragmentNotification("hello Kappa", fragments))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Emotes) != 1 || message.Emotes[0].Name != "Kappa" || message.Emotes[0].URL != "" {
		t.Fatalf("emotes=%+v", message.Emotes)
	}
	line := emote.FormatText(chat.PlatformTwitch, message.Text, message.Emotes, nil, func(chat.Platform, chat.Emote) (string, bool) {
		t.Fatal("invalid Twitch metadata reached graphical resolver")
		return "", false
	})
	if line.Text != "hello Kappa" || line.GraphicalText != "" || len(line.Images) != 0 {
		t.Fatalf("fallback=%+v", line)
	}
}

func TestParseEventMapsOnlyProvidedTwitchRoles(t *testing.T) {
	badges := `[{"set_id":"broadcaster","id":"1","info":""},{"set_id":"moderator","id":"1","info":""},{"set_id":"partner","id":"1","info":""},{"set_id":"vip","id":"1","info":""},{"set_id":"subscriber","id":"12","info":"12"}]`
	payload := strings.Replace(notification, `[{"set_id":"moderator","id":"1","info":""}]`, badges, 1)
	_, m, err := ParseEvent([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []chat.Role{chat.RoleBroadcaster, chat.RoleModerator, chat.RolePartner, chat.RoleVIP, chat.RoleSubscriber} {
		if !m.Roles.Has(role) {
			t.Fatalf("missing role %v in %v", role, m.Roles)
		}
	}
	if m.Roles.Has(chat.RoleFollower) || m.Roles.Has(chat.RoleOG) {
		t.Fatalf("inferred absent Twitch roles: %v", m.Roles)
	}
}

func TestTwitchCJKAndEmoteFallbackUseRuneRanges(t *testing.T) {
	payload := strings.Replace(notification, `"text":"Hi Kappa","fragments":[{"type":"text","text":"Hi "}`, `"text":"你好 Kappa","fragments":[{"type":"text","text":"你好 "}`, 1)
	_, m, err := ParseEvent([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Emotes[0]; got.Start != 3 || got.End != 7 || got.Name != "Kappa" {
		t.Fatalf("emote=%+v", got)
	}
	line := emote.FormatText(chat.PlatformTwitch, m.Text, m.Emotes, nil, nil)
	if line.Text != "你好 Kappa" || len(line.Images) != 0 {
		t.Fatalf("fallback=%+v", line)
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
	messages  [][]byte
	i         int
	deadlines int
}

type blockingWS struct {
	messages   [][]byte
	i          int
	closed     chan struct{}
	beforeRead func(int)
}

func (f *blockingWS) ReadMessage() (int, []byte, error) {
	if f.i < len(f.messages) {
		if f.beforeRead != nil {
			f.beforeRead(f.i)
		}
		message := f.messages[f.i]
		f.i++
		return 1, message, nil
	}
	<-f.closed
	return 0, nil, errors.New("closed")
}

func (f *blockingWS) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

func (f *blockingWS) SetReadDeadline(time.Time) error { return nil }

func (f *fakeWS) ReadMessage() (int, []byte, error) {
	if f.i >= len(f.messages) {
		return 0, nil, errors.New("done")
	}
	b := f.messages[f.i]
	f.i++
	return 1, b, nil
}
func (f *fakeWS) Close() error                    { return nil }
func (f *fakeWS) SetReadDeadline(time.Time) error { f.deadlines++; return nil }

func TestReconnectAndDuplicateDelivery(t *testing.T) {
	var subscribed int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eventsub/subscriptions" {
			t.Fatalf("path %s", r.URL.Path)
		}
		var request struct {
			Type      string            `json:"type"`
			Version   string            `json:"version"`
			Condition map[string]string `json:"condition"`
			Transport map[string]string `json:"transport"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Type != EventType || request.Version != "1" || request.Condition["broadcaster_user_id"] != "100" || request.Condition["user_id"] != "reader" || request.Transport["method"] != "websocket" || request.Transport["session_id"] != "session" {
			t.Fatalf("subscription=%+v", request)
		}
		subscribed++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer s.Close()
	a := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "token"}
	c := New(a, "ws://initial", "channel", "reader")
	welcome := []byte(`{"metadata":{"message_id":"welcome","message_type":"session_welcome","message_timestamp":"2026-01-01T00:00:00Z"},"payload":{"session":{"id":"session","keepalive_timeout_seconds":10}}}`)
	reconnect := []byte(`{"metadata":{"message_id":"reconnect","message_type":"session_reconnect","message_timestamp":"2026-01-01T00:00:01Z"},"payload":{"session":{"reconnect_url":"wss://new.example/ws"}}}`)
	keepalive := []byte(`{"metadata":{"message_id":"keepalive","message_type":"session_keepalive","message_timestamp":"2026-01-01T00:00:00.500Z"},"payload":{}}`)
	f := &fakeWS{messages: [][]byte{welcome, []byte(notification), []byte(notification), keepalive, reconnect}}
	out := make(chan chat.Message, 4)
	next, err := c.runSocket(context.Background(), f, "100", false, out)
	if err != nil {
		t.Fatal(err)
	}
	if next != "wss://new.example/ws" || subscribed != 1 || len(out) != 1 || f.deadlines < 4 {
		t.Fatalf("next=%q subscribed=%d messages=%d deadlines=%d", next, subscribed, len(out), f.deadlines)
	}
}

func TestInheritedReconnectWelcomeKeepsSubscriptionAndAdoptsSocket(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer s.Close()
	a := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "token"}
	c := New(a, "ws://initial", "channel", "reader")
	welcome := []byte(`{"metadata":{"message_id":"welcome-2","message_type":"session_welcome","message_timestamp":"2026-01-01T00:00:02Z"},"payload":{"session":{"id":"inherited","keepalive_timeout_seconds":10}}}`)
	reconnect := []byte(`{"metadata":{"message_id":"reconnect-2","message_type":"session_reconnect","message_timestamp":"2026-01-01T00:00:03Z"},"payload":{"session":{"reconnect_url":"wss://third.example/ws"}}}`)
	adopted := 0
	next, err := c.runSocketConnected(context.Background(), &fakeWS{messages: [][]byte{welcome, reconnect}}, "100", true, make(chan chat.Message, 1), func() { adopted++ })
	if err != nil || next != "wss://third.example/ws" || adopted != 1 || requests != 0 {
		t.Fatalf("next=%q adopted=%d requests=%d err=%v", next, adopted, requests, err)
	}
}

func TestReconnectContinuesReadingOldSocketUntilReplacementWelcome(t *testing.T) {
	var subscribed int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"100","login":"channel","display_name":"Channel"}]}`))
		case "/eventsub/subscriptions":
			subscribed++
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer s.Close()
	welcome := []byte(`{"metadata":{"message_id":"welcome","message_type":"session_welcome","message_timestamp":"2026-01-01T00:00:00Z"},"payload":{"session":{"id":"session","keepalive_timeout_seconds":10}}}`)
	reconnect := []byte(`{"metadata":{"message_id":"reconnect","message_type":"session_reconnect","message_timestamp":"2026-01-01T00:00:01Z"},"payload":{"session":{"reconnect_url":"wss://replacement.example/ws"}}}`)
	oldContinued := make(chan struct{})
	old := &blockingWS{messages: [][]byte{welcome, reconnect, []byte(notification)}, closed: make(chan struct{}), beforeRead: func(index int) {
		if index == 2 {
			close(oldContinued)
		}
	}}
	replacement := &blockingWS{messages: [][]byte{welcome}, closed: make(chan struct{}), beforeRead: func(index int) {
		if index == 0 {
			<-oldContinued
		}
	}}
	api := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "token"}
	client := New(api, "wss://initial.example/ws", "channel", "reader")
	var dialed []string
	client.Dial = func(_ context.Context, url string) (wsConn, error) {
		dialed = append(dialed, url)
		if len(dialed) == 1 {
			return old, nil
		}
		return replacement, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan chat.Message, 1)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, out) }()
	select {
	case message := <-out:
		if message.ID != "message-1" {
			t.Fatalf("message=%+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("notification from old socket was lost during reconnect")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
	if !reflect.DeepEqual(dialed, []string{"wss://initial.example/ws", "wss://replacement.example/ws"}) || subscribed != 1 {
		t.Fatalf("dialed=%v subscriptions=%d", dialed, subscribed)
	}
}

func TestRevocationReturnsAuthenticationError(t *testing.T) {
	a := &API{}
	c := New(a, "ws://initial", "channel", "reader")
	revocation := []byte(`{"metadata":{"message_id":"revoked","message_type":"revocation","message_timestamp":"2026-01-01T00:00:00Z","subscription_type":"channel.chat.message"},"payload":{"subscription":{"status":"authorization_revoked"}}}`)
	_, err := c.runSocket(context.Background(), &fakeWS{messages: [][]byte{revocation}}, "100", false, make(chan chat.Message, 1))
	var adapterErr *chat.AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Kind != chat.Authentication || !strings.Contains(err.Error(), "streamchat setup twitch") {
		t.Fatalf("revocation error=%v", err)
	}
}

func TestClientCancellationClosesEventSubSocket(t *testing.T) {
	subscribed := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"100","login":"channel","display_name":"Channel"}]}`))
		case "/eventsub/subscriptions":
			subscribed <- struct{}{}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer s.Close()
	api := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "token"}
	client := New(api, "ws://eventsub", "channel", "reader")
	socket := &blockingWS{messages: [][]byte{[]byte(`{"metadata":{"message_id":"welcome","message_type":"session_welcome","message_timestamp":"2026-01-01T00:00:00Z"},"payload":{"session":{"id":"session","keepalive_timeout_seconds":10}}}`)}, closed: make(chan struct{})}
	client.Dial = func(context.Context, string) (wsConn, error) { return socket, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, make(chan chat.Message, 1)) }()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("subscription was not created")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Twitch client did not stop after cancellation")
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("EventSub socket was not closed")
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
			w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":["user:read:chat","user:write:chat"]}`))
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

func TestReadOnlyAuthorizationIsInsufficientForSending(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer s.Close()
	sender := NewChatSender(&API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "secret"}, "100", "200", []string{ReadChatScope})
	err := sender.Send(context.Background(), "hello")
	if !errors.Is(err, ErrWriteScope) || !strings.Contains(err.Error(), "streamchat setup twitch") || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestSendChatMessageRequest(t *testing.T) {
	var body map[string]string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/messages" {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("Client-Id") != "client-id" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers=%v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"message_id":"message-id","is_sent":true,"drop_reason":null}]}`))
	}))
	defer s.Close()
	sender := NewChatSender(&API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client-id", AccessToken: "access-token"}, "broadcaster-id", "sender-id", RequiredChatScopes)
	messageID, err := sender.SendMessage(context.Background(), "hello 世界")
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "message-id" {
		t.Fatalf("message ID=%q", messageID)
	}
	want := map[string]string{"broadcaster_id": "broadcaster-id", "sender_id": "sender-id", "message": "hello 世界"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body=%v want=%v", body, want)
	}
}

func TestSendChatRefreshesAndRetriesOnce(t *testing.T) {
	chatRequests := 0
	persisted := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/messages":
			chatRequests++
			if chatRequests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatalf("retry authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"message_id":"sent","is_sent":true}]}`))
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "old-refresh" {
				t.Fatalf("refresh form=%v err=%v", r.Form, err)
			}
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":["user:read:chat","user:write:chat"]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer s.Close()
	api := &API{HTTP: s.Client(), APIBaseURL: s.URL, OAuthBaseURL: s.URL, ClientID: "client", ClientSecret: "secret", AccessToken: "old-access", RefreshToken: "old-refresh", OnToken: func(tok Token) error { persisted = tok.RefreshToken == "new-refresh"; return nil }}
	sender := NewChatSender(api, "100", "200", RequiredChatScopes)
	if err := sender.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if chatRequests != 2 || api.RefreshToken != "new-refresh" || !persisted {
		t.Fatalf("chatRequests=%d refresh=%q persisted=%t", chatRequests, api.RefreshToken, persisted)
	}
}

func TestSendChatRefreshRejectsLostWriteScopeWithoutRetry(t *testing.T) {
	chatRequests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/messages" {
			chatRequests++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":["user:read:chat"]}`))
	}))
	defer s.Close()
	api := &API{HTTP: s.Client(), APIBaseURL: s.URL, OAuthBaseURL: s.URL, ClientID: "client", ClientSecret: "secret", AccessToken: "old", RefreshToken: "refresh"}
	err := NewChatSender(api, "100", "200", RequiredChatScopes).Send(context.Background(), "hello")
	if !errors.Is(err, ErrWriteScope) || chatRequests != 1 {
		t.Fatalf("err=%v chatRequests=%d", err, chatRequests)
	}
}

func TestSendChatFailuresAreConciseAndDoNotLeakCredentials(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"authorization", http.StatusUnauthorized, ErrChatAuthentication},
		{"forbidden", http.StatusForbidden, ErrChatRejected},
		{"rate limit", http.StatusTooManyRequests, ErrChatRateLimit},
		{"bad request", http.StatusBadRequest, ErrChatRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"message":"access-token client-secret refresh-token"}`))
			}))
			defer s.Close()
			api := &API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client-secret", AccessToken: "access-token"}
			err := NewChatSender(api, "100", "200", RequiredChatScopes).Send(context.Background(), "hello")
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			for _, secret := range []string{"access-token", "client-secret", "refresh-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("credential leaked: %v", err)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSendChatNetworkErrorDoesNotLeakHeaders(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	api := &API{HTTP: client, APIBaseURL: "https://api.twitch.tv/helix", ClientID: "client-secret-value", AccessToken: "access-secret-value"}
	err := NewChatSender(api, "100", "200", RequiredChatScopes).Send(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "network unavailable") || strings.Contains(err.Error(), "client-secret-value") || strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("err=%v", err)
	}
}

func TestSendChatReportsProviderDropReason(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"message_id":"message-id","is_sent":false,"drop_reason":{"code":"automod_held","message":"not sent"}}]}`))
	}))
	defer s.Close()
	err := NewChatSender(&API{HTTP: s.Client(), APIBaseURL: s.URL, ClientID: "client", AccessToken: "access"}, "100", "200", RequiredChatScopes).Send(context.Background(), "hello")
	if !errors.Is(err, ErrChatRejected) || !strings.Contains(err.Error(), "automod_held") {
		t.Fatalf("err=%v", err)
	}
}
