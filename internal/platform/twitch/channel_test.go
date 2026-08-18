package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func twitchChannelClient(server *httptest.Server, scopes []string, broadcasterID, userID string) *ChannelClient {
	api := &API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", ClientSecret: "secret", AccessToken: "access", RefreshToken: "refresh"}
	return NewChannelClient(NewUserClient(api, scopes), broadcasterID, userID)
}

func requireTwitchHeaders(t *testing.T, request *http.Request, token string) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Client-Id") != "client" {
		t.Fatalf("headers authorization=%q client-id=%q", request.Header.Get("Authorization"), request.Header.Get("Client-Id"))
	}
}

func TestChannelStatusCombinesChannelInformationAndLiveStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requireTwitchHeaders(t, request, "access")
		switch request.URL.Path {
		case "/channels":
			if request.Method != http.MethodGet || request.URL.Query().Get("broadcaster_id") != "100" {
				t.Fatalf("channel request=%s %s", request.Method, request.URL.String())
			}
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_id":"100","game_id":"509658","game_name":"Just Chatting","title":"Twitch title"}]}`)
		case "/streams":
			if request.Method != http.MethodGet || request.URL.Query().Get("user_id") != "100" {
				t.Fatalf("stream request=%s %s", request.Method, request.URL.String())
			}
			_, _ = io.WriteString(w, `{"data":[{"user_id":"100","type":"live","viewer_count":123}]}`)
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	defer server.Close()
	status, err := twitchChannelClient(server, RequiredChatScopes, "100", "100").GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ChannelStatus{Title: "Twitch title", CategoryID: "509658", Category: "Just Chatting", ViewerCount: 123, Live: true}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("status=%+v want=%+v", status, want)
	}
}

func TestChannelStatusUsesOfflineStateWithoutViewerCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/channels":
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_id":"100","game_id":"1","game_name":"Games","title":"Offline title"}]}`)
		case "/streams":
			_, _ = io.WriteString(w, `{"data":[]}`)
		}
	}))
	defer server.Close()
	status, err := twitchChannelClient(server, RequiredChatScopes, "100", "100").GetStatus(context.Background())
	if err != nil || status.Live || status.ViewerCount != 0 || status.Title != "Offline title" || status.Category != "Games" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestChannelStatusRejectsMissingChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	_, err := twitchChannelClient(server, RequiredChatScopes, "missing", "100").GetStatus(context.Background())
	if !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestChannelStatusRefreshesAndRetriesOnceWithoutManagementScope(t *testing.T) {
	var channelRequests, refreshes, persisted int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/channels":
			channelRequests++
			if request.Header.Get("Authorization") == "Bearer access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			requireTwitchHeaders(t, request, "new-access")
			_, _ = io.WriteString(w, `{"data":[{"broadcaster_id":"100","game_id":"1","game_name":"Games","title":"Refreshed"}]}`)
		case "/streams":
			requireTwitchHeaders(t, request, "new-access")
			_, _ = io.WriteString(w, `{"data":[]}`)
		case "/token":
			refreshes++
			_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":["user:read:chat","user:write:chat"]}`)
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	defer server.Close()
	client := twitchChannelClient(server, RequiredChatScopes, "100", "100")
	client.Auth.API.OnToken = func(token Token) error {
		persisted++
		if token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" {
			t.Fatalf("token=%+v", token)
		}
		return nil
	}
	status, err := client.GetStatus(context.Background())
	if err != nil || status.Title != "Refreshed" || channelRequests != 2 || refreshes != 1 || persisted != 1 {
		t.Fatalf("status=%+v channelRequests=%d refreshes=%d persisted=%d err=%v", status, channelRequests, refreshes, persisted, err)
	}
}

func TestChannelStatusClassifiesRateLimitAuthenticationAndAPIErrors(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrChannelAuthentication},
		{http.StatusTooManyRequests, ErrChannelRateLimit},
		{http.StatusInternalServerError, nil},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) }))
			defer server.Close()
			client := twitchChannelClient(server, RequiredChatScopes, "100", "100")
			client.Auth.API.RefreshToken = ""
			_, err := client.GetStatus(context.Background())
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("status=%d err=%v", test.status, err)
			}
		})
	}
}

func TestUpdateTitleUsesOfficialEndpointPayloadAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/channels" || request.URL.Query().Get("broadcaster_id") != "100" {
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
		requireTwitchHeaders(t, request, "access")
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type=%q", request.Header.Get("Content-Type"))
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || !reflect.DeepEqual(payload, map[string]string{"title": "New title"}) {
			t.Fatalf("payload=%v err=%v", payload, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := twitchChannelClient(server, SetupScopes, "100", "100").UpdateTitle(context.Background(), "New title"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateTitleRefreshesAndRetriesOnce(t *testing.T) {
	var patches, refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/channels":
			patches++
			if request.Header.Get("Authorization") == "Bearer access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			requireTwitchHeaders(t, request, "new-access")
			w.WriteHeader(http.StatusNoContent)
		case "/token":
			refreshes++
			_, _ = io.WriteString(w, `{"access_token":"new-access","expires_in":3600,"scope":["user:read:chat","user:write:chat","channel:manage:broadcast"]}`)
		}
	}))
	defer server.Close()
	if err := twitchChannelClient(server, SetupScopes, "100", "100").UpdateTitle(context.Background(), "New title"); err != nil || patches != 2 || refreshes != 1 {
		t.Fatalf("patches=%d refreshes=%d err=%v", patches, refreshes, err)
	}
}

func TestChannelUpdateClassifiesRateLimitAndAPIRejection(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusTooManyRequests, ErrChannelRateLimit},
		{http.StatusBadRequest, nil},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) }))
			defer server.Close()
			err := twitchChannelClient(server, SetupScopes, "100", "100").UpdateTitle(context.Background(), "title")
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("status=%d err=%v", test.status, err)
			}
		})
	}
}

func TestMissingManagementScopeBlocksControlsButDoesNotBreakChat(t *testing.T) {
	var chatRequests, channelRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/chat/messages":
			chatRequests++
			_, _ = io.WriteString(w, `{"data":[{"message_id":"message","is_sent":true}]}`)
		case "/channels":
			channelRequests++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	api := &API{HTTP: server.Client(), APIBaseURL: server.URL, OAuthBaseURL: server.URL, ClientID: "client", AccessToken: "access"}
	auth := NewUserClient(api, RequiredChatScopes)
	chat := NewChatSenderWithUserClient(auth, "100", "100")
	channel := NewChannelClient(auth, "100", "100")
	if err := channel.UpdateTitle(context.Background(), "blocked"); !errors.Is(err, ErrManageScope) || !strings.Contains(err.Error(), "streamchat setup twitch") {
		t.Fatalf("management error=%v", err)
	}
	if err := chat.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if chatRequests != 1 || channelRequests != 0 {
		t.Fatalf("chat requests=%d channel requests=%d", chatRequests, channelRequests)
	}
}

func TestChannelUpdateRejectsBroadcasterMismatchLocally(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	err := twitchChannelClient(server, SetupScopes, "100", "200").UpdateTitle(context.Background(), "title")
	if !errors.Is(err, ErrChannelOwnership) || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestResolveNumericCategoryAndUpdateCanonicalGameID(t *testing.T) {
	var patched map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/games":
			if request.URL.Query().Get("id") != "509658" {
				t.Fatalf("query=%s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"509658","name":"Just Chatting"}]}`)
		case request.Method == http.MethodPatch && request.URL.Path == "/channels":
			_ = json.NewDecoder(request.Body).Decode(&patched)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	client := twitchChannelClient(server, SetupScopes, "100", "100")
	category, err := client.ResolveCategory(context.Background(), "509658")
	if err != nil || category != (Category{ID: "509658", Name: "Just Chatting"}) {
		t.Fatalf("category=%+v err=%v", category, err)
	}
	if err = client.UpdateCategory(context.Background(), category.ID); err != nil || !reflect.DeepEqual(patched, map[string]string{"game_id": "509658"}) {
		t.Fatalf("payload=%v err=%v", patched, err)
	}
}

func TestResolveCategoryRejectsInvalidNumericIDs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	client := twitchChannelClient(server, SetupScopes, "100", "100")
	if _, err := client.ResolveCategory(context.Background(), "0"); err == nil || requests != 0 {
		t.Fatalf("zero-ID err=%v requests=%d", err, requests)
	}
	if _, err := client.ResolveCategory(context.Background(), "999999"); !errors.Is(err, ErrCategoryNotFound) || requests != 1 {
		t.Fatalf("unknown-ID err=%v requests=%d", err, requests)
	}
}

func TestCategoryLookupClassifiesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer server.Close()
	_, err := twitchChannelClient(server, SetupScopes, "100", "100").ResolveCategory(context.Background(), "Just Chatting")
	if !errors.Is(err, ErrChannelRateLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveCategoryNameUsesExactCanonicalMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search/categories" || request.URL.Query().Get("query") != "just chatting" || request.URL.Query().Get("first") != "100" {
			t.Fatalf("request=%s", request.URL.String())
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"1","name":"Chatting"},{"id":"509658","name":"Just Chatting"}]}`)
	}))
	defer server.Close()
	category, err := twitchChannelClient(server, SetupScopes, "100", "100").ResolveCategory(context.Background(), "just chatting")
	if err != nil || category.Name != "Just Chatting" || category.ID != "509658" {
		t.Fatalf("category=%+v err=%v", category, err)
	}
}

func TestResolveCategoryRejectsNoMatchAndAmbiguousSearch(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		check    func(error) bool
	}{
		{"no match", `{"data":[]}`, func(err error) bool { return errors.Is(err, ErrCategoryNotFound) }},
		{"non-exact single match", `{"data":[{"id":"1","name":"Just Chatting IRL"}]}`, func(err error) bool { var ambiguous *AmbiguousCategoryError; return errors.As(err, &ambiguous) }},
		{"multiple matches", `{"data":[{"id":"1","name":"Chatting"},{"id":"2","name":"Just Chatting IRL"}]}`, func(err error) bool { var ambiguous *AmbiguousCategoryError; return errors.As(err, &ambiguous) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.response) }))
			defer server.Close()
			_, err := twitchChannelClient(server, SetupScopes, "100", "100").ResolveCategory(context.Background(), "chat")
			if !test.check(err) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestChannelRequestsDoNotLeakCredentialsInErrors(t *testing.T) {
	secret := "super-secret-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	client := twitchChannelClient(server, SetupScopes, "100", "100")
	client.Auth.API.AccessToken = secret
	_, err := client.GetStatus(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestNumericCategoryDetection(t *testing.T) {
	for input, want := range map[string]bool{"123": true, "0": true, "": false, "12a": false, "１２３": false} {
		if got := numeric(input); got != want {
			t.Fatalf("numeric(%q)=%v want=%v", input, got, want)
		}
	}
}
