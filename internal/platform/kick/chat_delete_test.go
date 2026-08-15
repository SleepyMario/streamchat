package kick

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatDeleteUsesOfficialEndpoint(t *testing.T) {
	var method, path, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, authorization = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := ChatDeleteClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
	if err := client.DeleteMessage(context.Background(), "message/id"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/chat/message%2Fid" || authorization != "Bearer access-token" {
		t.Fatalf("method=%q path=%q authorization=%q", method, path, authorization)
	}
}

func TestChatDeleteStatusErrorsAreSanitized(t *testing.T) {
	tests := []struct {
		status   int
		want     error
		wantText string
	}{
		{status: http.StatusUnauthorized, want: ErrChatDeleteAuthentication},
		{status: http.StatusForbidden, want: ErrChatDeleteScope},
		{status: http.StatusNotFound, want: ErrChatDeleteNotFound},
		{status: http.StatusTooManyRequests, want: ErrChatDeleteRateLimit},
		{status: http.StatusInternalServerError, wantText: "Kick chat-message deletion failed (HTTP 500)"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`access_token=secret-provider-body`))
			}))
			defer server.Close()
			err := (ChatDeleteClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "token"}).DeleteMessage(context.Background(), "id")
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.wantText != "" && err.Error() != test.wantText {
				t.Fatalf("error=%q want=%q", err, test.wantText)
			}
			if strings.Contains(err.Error(), "secret-provider-body") || strings.Contains(err.Error(), "access_token") {
				t.Fatalf("provider response leaked: %v", err)
			}
		})
	}
}
