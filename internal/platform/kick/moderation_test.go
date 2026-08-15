package kick

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseTimeoutDurationUsesOfficialMinuteBounds(t *testing.T) {
	tests := []struct {
		value       string
		wantMinutes int64
		wantError   bool
	}{
		{value: "1s", wantError: true},
		{value: "30s", wantError: true},
		{value: "60s", wantMinutes: 1},
		{value: "10m", wantMinutes: 10},
		{value: "1h", wantMinutes: 60},
		{value: "168h", wantMinutes: 10080},
		{value: "169h", wantError: true},
		{value: "0m", wantError: true},
		{value: "ten", wantError: true},
		{value: "10", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			minutes, err := ParseTimeoutDuration(test.value)
			if test.wantError && err == nil {
				t.Fatalf("minutes=%d; expected error", minutes)
			}
			if !test.wantError && (err != nil || minutes != test.wantMinutes) {
				t.Fatalf("minutes=%d err=%v", minutes, err)
			}
		})
	}
}

func TestModerationBanAndTimeoutUseExactOfficialBodies(t *testing.T) {
	tests := []struct {
		name     string
		duration *int64
		wantBody string
	}{
		{name: "ban", wantBody: `{"broadcaster_user_id":123,"user_id":456}`},
		{name: "timeout", duration: pointer(int64(10)), wantBody: `{"broadcaster_user_id":123,"user_id":456,"duration":10}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer access-token" {
					t.Fatal("missing bearer authorization")
				}
				switch requests {
				case 1:
					if r.Method != http.MethodGet || r.URL.Path != "/channels" || r.URL.Query().Get("slug") != "TargetUser" {
						t.Fatalf("lookup request: %s %s", r.Method, r.URL.String())
					}
					_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"targetuser"}]}`)
				case 2:
					if r.Method != http.MethodPost || r.URL.Path != "/moderation/bans" || r.Header.Get("Content-Type") != "application/json" {
						t.Fatalf("moderation request: %s %s", r.Method, r.URL.String())
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if string(body) != test.wantBody {
						t.Fatalf("body=%s", body)
					}
					_, _ = io.WriteString(w, `{"data":{},"message":"OK"}`)
				default:
					t.Fatalf("unexpected request %d", requests)
				}
			}))
			defer server.Close()
			client := ModerationClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			var user ModerationUser
			var err error
			if test.duration == nil {
				user, err = client.Ban(context.Background(), "123", "TargetUser")
			} else {
				user, err = client.Timeout(context.Background(), "123", "TargetUser", *test.duration)
			}
			if err != nil || user.ID != 456 || user.Username != "targetuser" || requests != 2 {
				t.Fatalf("user=%+v requests=%d err=%v", user, requests, err)
			}
		})
	}
}

func TestResolveModerationUserRejectsUnknownAndAmbiguousResults(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		wantError error
		ambiguous bool
	}{
		{name: "unknown", response: `{"data":[]}`, wantError: ErrModerationUserNotFound},
		{name: "non-exact", response: `{"data":[{"broadcaster_user_id":1,"slug":"someone-else"}]}`, wantError: ErrModerationUserNotFound},
		{name: "ambiguous", response: `{"data":[{"broadcaster_user_id":1,"slug":"user"},{"broadcaster_user_id":2,"slug":"USER"}]}`, ambiguous: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			client := ModerationClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			_, err := client.ResolveUser(context.Background(), "user")
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("err=%v", err)
			}
			if test.ambiguous {
				var target *AmbiguousModerationUserError
				if !errors.As(err, &target) {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestModerationAuthenticationScopeAndPermissionFailures(t *testing.T) {
	client := ModerationClient{BaseURL: "https://api.invalid"}
	if _, err := client.Ban(context.Background(), "123", "user"); !errors.Is(err, ErrModerationAuthentication) {
		t.Fatalf("authentication err=%v", err)
	}

	tests := []struct {
		name       string
		lookupCode int
		actionCode int
		header     string
		want       error
	}{
		{name: "missing channel read scope", lookupCode: http.StatusForbidden, want: ErrModerationScope},
		{name: "missing moderation scope", actionCode: http.StatusForbidden, header: `Bearer error="insufficient_scope"`, want: ErrModerationScope},
		{name: "insufficient channel permission", actionCode: http.StatusForbidden, want: ErrModerationPermission},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if test.lookupCode != 0 {
						w.WriteHeader(test.lookupCode)
						_, _ = io.WriteString(w, `{"message":"secret-provider-response"}`)
						return
					}
					_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"user"}]}`)
					return
				}
				w.Header().Set("WWW-Authenticate", test.header)
				w.WriteHeader(test.actionCode)
				_, _ = io.WriteString(w, `{"message":"secret-provider-response"}`)
			}))
			defer server.Close()
			client := ModerationClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			_, err := client.Ban(context.Background(), "123", "user")
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "secret-provider-response") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestModerationRateLimitAndAPIErrorsAreSanitized(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":456,"slug":"user"}]}`)
					return
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"access_token":"secret-token","message":"secret-provider-response"}`)
			}))
			defer server.Close()
			client := ModerationClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			_, err := client.Ban(context.Background(), "123", "user")
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err=%v", err)
			}
			if status == http.StatusTooManyRequests && !errors.Is(err, ErrModerationRateLimit) {
				t.Fatalf("rate limit err=%v", err)
			}
		})
	}
}

func pointer[T any](value T) *T { return &value }
