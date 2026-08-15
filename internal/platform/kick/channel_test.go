package kick

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateTitleUsesExactPatchBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/channels" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("missing channel update headers")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"stream_title":"New title"}` {
			t.Fatalf("body=%s", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
	if err := client.UpdateTitle(context.Background(), "New title"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateTitleAuthenticationAndPermissionFailures(t *testing.T) {
	client := ChannelClient{BaseURL: "https://api.invalid"}
	if err := client.UpdateTitle(context.Background(), "title"); !errors.Is(err, ErrChannelAuthentication) {
		t.Fatalf("auth err=%v", err)
	}
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer authServer.Close()
	client = ChannelClient{HTTP: authServer.Client(), BaseURL: authServer.URL, AccessToken: "expired-token"}
	if err := client.UpdateTitle(context.Background(), "title"); !errors.Is(err, ErrChannelAuthentication) {
		t.Fatalf("HTTP auth err=%v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"secret-provider-response"}`)
	}))
	defer server.Close()
	client = ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
	err := client.UpdateTitle(context.Background(), "title")
	if !errors.Is(err, ErrChannelWritePermission) || !strings.Contains(err.Error(), ChannelWriteScope) || strings.Contains(err.Error(), "secret-provider-response") {
		t.Fatalf("permission err=%v", err)
	}
}

func TestResolveNumericCategoryID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/categories/123" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = io.WriteString(w, `{"data":{"id":123,"name":"Just Chatting"}}`)
	}))
	defer server.Close()
	client := ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
	category, err := client.ResolveCategory(context.Background(), "123")
	if err != nil || category.ID != 123 || category.Name != "Just Chatting" {
		t.Fatalf("category=%+v err=%v", category, err)
	}
}

func TestResolveCategoryExactNoMatchAndAmbiguous(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		want      Category
		wantErr   error
		ambiguous bool
	}{
		{name: "exact", response: `{"data":[{"id":1,"name":"Chatting"},{"id":2,"name":"Just Chatting"}]}`, want: Category{ID: 2, Name: "Just Chatting"}},
		{name: "no match", response: `{"data":[]}`, wantErr: ErrCategoryNotFound},
		{name: "ambiguous", response: `{"data":[{"id":1,"name":"Games"},{"id":2,"name":"Gaming"}]}`, ambiguous: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/categories" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
				}
				query := r.URL.Query().Get("q")
				if (test.name == "exact" && query != "Just Chatting") || (test.name != "exact" && query != "Gam") {
					t.Fatalf("query=%q", query)
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			client := ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			argument := "Gam"
			if test.name == "exact" {
				argument = "Just Chatting"
			}
			category, err := client.ResolveCategory(context.Background(), argument)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v", err)
			}
			if test.ambiguous {
				var ambiguous *AmbiguousCategoryError
				if !errors.As(err, &ambiguous) || !strings.Contains(err.Error(), "1. Games (1)") || !strings.Contains(err.Error(), "2. Gaming (2)") {
					t.Fatalf("err=%v", err)
				}
			}
			if test.want.ID != 0 && category != test.want {
				t.Fatalf("category=%+v", category)
			}
		})
	}
}

func TestUpdateCategoryUsesExactPatchBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/channels" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["category_id"] != float64(123) {
			t.Fatalf("body=%v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
	if err := client.UpdateCategory(context.Background(), 123); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryAPIFailuresAreSanitized(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"secret-provider-response"}`)
			}))
			defer server.Close()
			client := ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "access-token"}
			var err error
			if status == http.StatusTooManyRequests {
				err = client.UpdateCategory(context.Background(), 123)
			} else {
				_, err = client.SearchCategories(context.Background(), "Games")
			}
			if err == nil || strings.Contains(err.Error(), "secret-provider-response") {
				t.Fatalf("err=%v", err)
			}
			if status == http.StatusTooManyRequests && !errors.Is(err, ErrChannelRateLimit) {
				t.Fatalf("rate limit err=%v", err)
			}
		})
	}
}

func TestGetChannelStatusLiveAndOffline(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		want     ChannelStatus
	}{
		{"live", `{"data":[{"slug":"sleepymario","stream_title":"Live title","category":{"name":"Just Chatting"},"stream":{"is_live":true,"viewer_count":42}}]}`, ChannelStatus{Slug: "sleepymario", Title: "Live title", Category: "Just Chatting", Live: true, ViewerCount: 42}},
		{"offline", `{"data":[{"stream_title":"Offline title","category":{"name":"Games"},"stream":null}]}`, ChannelStatus{Title: "Offline title", Category: "Games"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/channels" || r.URL.RawQuery != "" {
					t.Fatalf("request=%s %s", r.Method, r.URL.String())
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			got, err := (ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "token"}).GetStatus(context.Background())
			if err != nil || got != test.want {
				t.Fatalf("status=%+v err=%v", got, err)
			}
		})
	}
}

func TestGetChannelStatusFailureIsSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"access_token=provider-secret"}`)
	}))
	defer server.Close()
	_, err := (ChannelClient{HTTP: server.Client(), BaseURL: server.URL, AccessToken: "token"}).GetStatus(context.Background())
	if !errors.Is(err, ErrChannelReadPermission) || strings.Contains(err.Error(), "provider-secret") || strings.Contains(err.Error(), "access_token") {
		t.Fatalf("error=%v", err)
	}
}
