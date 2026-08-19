package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func twitchModerationClient(server *httptest.Server, scopes []string, broadcasterID, moderatorID string) *ModerationClient {
	api := &API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", ClientSecret: "secret", AccessToken: "access", RefreshToken: "refresh"}
	return NewModerationClient(NewUserClient(api, scopes), broadcasterID, moderatorID)
}

func writeTwitchUser(w http.ResponseWriter, id, login, displayName string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": id, "login": login, "display_name": displayName}}})
}

func TestBanUsesOfficialEndpointResolvedUserAndPermanentPayload(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requireTwitchHeaders(t, request, "access")
		requests = append(requests, request.Method+" "+request.URL.String())
		switch request.URL.Path {
		case "/users":
			if request.Method != http.MethodGet || request.URL.Query().Get("login") != "targetuser" {
				t.Fatalf("user request=%s %s", request.Method, request.URL.String())
			}
			writeTwitchUser(w, "300", "targetuser", "TargetUser")
		case "/moderation/bans":
			if request.Method != http.MethodPost || request.URL.Query().Get("broadcaster_id") != "100" || request.URL.Query().Get("moderator_id") != "200" {
				t.Fatalf("ban request=%s %s", request.Method, request.URL.String())
			}
			if request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("content-type=%q", request.Header.Get("Content-Type"))
			}
			var payload struct {
				Data map[string]any `json:"data"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Data["user_id"] != "300" {
				t.Fatalf("payload=%v", payload.Data)
			}
			if _, exists := payload.Data["duration"]; exists {
				t.Fatalf("permanent ban included duration: %v", payload.Data)
			}
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_id":"100","moderator_id":"200","user_id":"300"}]}`)
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	defer server.Close()
	user, err := twitchModerationClient(server, SetupScopes, "100", "200").Ban(context.Background(), "@TARGETUSER")
	if err != nil || user.ID != "300" || user.Login != "targetuser" || user.DisplayName != "TargetUser" || len(requests) != 2 {
		t.Fatalf("user=%+v requests=%v err=%v", user, requests, err)
	}
}

func TestTwitchTimeoutDurationSemantics(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
	}{{"1s", 1}, {"30s", 30}, {"10m", 600}, {"1h", 3600}, {"1d", 86400}, {"14d", MaxTimeoutSeconds}} {
		got, err := ParseTimeoutDuration(test.value)
		if err != nil || got != test.want {
			t.Fatalf("duration=%q got=%d want=%d err=%v", test.value, got, test.want, err)
		}
	}
	for _, value := range []string{"", "0s", "-1s", "1", "1ms", "15d", "1209601s", "1.5h", "forever"} {
		if got, err := ParseTimeoutDuration(value); err == nil || got != 0 {
			t.Fatalf("duration=%q got=%d err=%v", value, got, err)
		}
	}
}

func TestTimeoutSendsWholeSeconds(t *testing.T) {
	for _, test := range []struct {
		value   string
		seconds int64
	}{{"1s", 1}, {"30s", 30}, {"10m", 600}, {"1h", 3600}, {"14d", MaxTimeoutSeconds}} {
		t.Run(test.value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/users":
					writeTwitchUser(w, "300", "target", "Target")
				case "/moderation/bans":
					var payload struct {
						Data struct {
							UserID   string `json:"user_id"`
							Duration int64  `json:"duration"`
						} `json:"data"`
					}
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Data.UserID != "300" || payload.Data.Duration != test.seconds {
						t.Fatalf("payload=%+v err=%v", payload, err)
					}
					_, _ = io.WriteString(w, `{"data":[{}]}`)
				}
			}))
			defer server.Close()
			seconds, _ := ParseTimeoutDuration(test.value)
			if _, err := twitchModerationClient(server, SetupScopes, "100", "200").Timeout(context.Background(), "target", seconds); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestModerationRefreshesAndRetriesOnceAndPersistsToken(t *testing.T) {
	var bans, refreshes, persisted int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users":
			writeTwitchUser(w, "300", "target", "Target")
		case "/moderation/bans":
			bans++
			if request.Header.Get("Authorization") == "Bearer access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			requireTwitchHeaders(t, request, "new-access")
			_, _ = io.WriteString(w, `{"data":[{}]}`)
		case "/token":
			refreshes++
			_ = json.NewEncoder(w).Encode(Token{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600, Scopes: SetupScopes})
		}
	}))
	defer server.Close()
	client := twitchModerationClient(server, SetupScopes, "100", "200")
	client.Auth.API.OnToken = func(token Token) error {
		persisted++
		if token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" {
			t.Fatalf("token=%+v", token)
		}
		return nil
	}
	if _, err := client.Ban(context.Background(), "target"); err != nil || bans != 2 || refreshes != 1 || persisted != 1 {
		t.Fatalf("bans=%d refreshes=%d persisted=%d err=%v", bans, refreshes, persisted, err)
	}
}

func TestClearRefreshesAndRetriesOnce(t *testing.T) {
	var deletes, refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/moderation/chat":
			deletes++
			if request.Header.Get("Authorization") == "Bearer access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			requireTwitchHeaders(t, request, "new-access")
			w.WriteHeader(http.StatusNoContent)
		case "/token":
			refreshes++
			_ = json.NewEncoder(w).Encode(Token{AccessToken: "new-access", ExpiresIn: 3600, Scopes: SetupScopes})
		}
	}))
	defer server.Close()
	if err := twitchModerationClient(server, SetupScopes, "100", "200").ClearChat(context.Background()); err != nil || deletes != 2 || refreshes != 1 {
		t.Fatalf("deletes=%d refreshes=%d err=%v", deletes, refreshes, err)
	}
}

func TestMissingModerationScopesDoNotBreakChat(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path == "/chat/messages" {
			_, _ = io.WriteString(w, `{"data":[{"message_id":"message","is_sent":true}]}`)
		}
	}))
	defer server.Close()
	api := &API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "access"}
	auth := NewUserClient(api, RequiredChatScopes)
	moderation := NewModerationClient(auth, "100", "200")
	if _, err := moderation.Ban(context.Background(), "target"); !errors.Is(err, ErrBannedUsersScope) || !strings.Contains(err.Error(), "streamchat setup twitch") {
		t.Fatalf("ban error=%v", err)
	}
	if err := moderation.ClearChat(context.Background()); !errors.Is(err, ErrChatMessagesScope) || !strings.Contains(err.Error(), "streamchat setup twitch") {
		t.Fatalf("clear error=%v", err)
	}
	if err := NewChatSenderWithUserClient(auth, "100", "200").Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestBanClassifiesPermissionConflictAlreadyBannedAndRateLimit(t *testing.T) {
	for _, test := range []struct {
		status  int
		message string
		want    error
	}{{http.StatusForbidden, "", ErrModerationPermission}, {http.StatusConflict, "", ErrModerationConflict}, {http.StatusBadRequest, "The user is already banned.", ErrAlreadyBanned}, {http.StatusTooManyRequests, "", ErrModerationRateLimit}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/users" {
					writeTwitchUser(w, "300", "target", "Target")
					return
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": test.message})
			}))
			defer server.Close()
			_, err := twitchModerationClient(server, SetupScopes, "100", "200").Ban(context.Background(), "target")
			if !errors.Is(err, test.want) {
				t.Fatalf("status=%d err=%v", test.status, err)
			}
		})
	}
}

func TestBanRejectsBroadcasterAndSelfLocally(t *testing.T) {
	for _, targetID := range []string{"100", "200"} {
		posts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/users" {
				writeTwitchUser(w, targetID, "target", "Target")
				return
			}
			posts++
		}))
		defer server.Close()
		_, err := twitchModerationClient(server, SetupScopes, "100", "200").Ban(context.Background(), "target")
		if !errors.Is(err, ErrModerationTarget) || posts != 0 {
			t.Fatalf("target=%s posts=%d err=%v", targetID, posts, err)
		}
	}
}

func TestModerationUserNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users" {
			t.Fatalf("unexpected request=%s", request.URL.String())
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	_, err := twitchModerationClient(server, SetupScopes, "100", "200").Ban(context.Background(), "missing")
	if !errors.Is(err, ErrModerationUserNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestClearChatAndSpecificMessageUseOfficialEndpoint(t *testing.T) {
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.URL.Path != "/moderation/chat" {
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
		requireTwitchHeaders(t, request, "access")
		queries = append(queries, request.URL.Query())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := twitchModerationClient(server, SetupScopes, "100", "200")
	if err := client.ClearChat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteMessage(context.Background(), "message-1"); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0].Get("broadcaster_id") != "100" || queries[0].Get("moderator_id") != "200" || queries[0].Has("message_id") || queries[1].Get("message_id") != "message-1" {
		t.Fatalf("queries=%v", queries)
	}
}

func TestClearClassifiesExpectedFailures(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{http.StatusBadRequest, ErrChatDeleteProtected}, {http.StatusUnauthorized, ErrModerationAuthentication}, {http.StatusForbidden, ErrModerationPermission}, {http.StatusNotFound, ErrChatDeleteNotFound}, {http.StatusTooManyRequests, ErrModerationRateLimit}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) }))
			defer server.Close()
			client := twitchModerationClient(server, SetupScopes, "100", "200")
			client.Auth.API.RefreshToken = ""
			if err := client.DeleteMessage(context.Background(), "message"); !errors.Is(err, test.want) {
				t.Fatalf("status=%d err=%v", test.status, err)
			}
		})
	}
}

func TestModerationErrorsDoNotLeakCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"access-token refresh-token client-secret"}`)
	}))
	defer server.Close()
	client := twitchModerationClient(server, SetupScopes, "100", "200")
	client.Auth.API.RefreshToken = ""
	err := client.DeleteMessage(context.Background(), "message")
	if err == nil || strings.Contains(err.Error(), "access-token") || strings.Contains(err.Error(), "refresh-token") || strings.Contains(err.Error(), "client-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestSetupScopesOrderIsStableAndExact(t *testing.T) {
	want := []string{ReadChatScope, WriteChatScope, ManageBroadcastScope, ManageBannedUsersScope, ManageChatMessagesScope}
	if !reflect.DeepEqual(SetupScopes, want) {
		t.Fatalf("scopes=%v want=%v", SetupScopes, want)
	}
}
