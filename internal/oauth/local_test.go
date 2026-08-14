package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizationURLStateScopesAndPKCE(t *testing.T) {
	u, err := AuthorizationURL(Request{AuthorizeURL: "https://id.example/authorize", ClientID: "client", RedirectURI: "http://localhost:9999/callback", Scopes: []string{"user:read", "events:subscribe"}, UsePKCE: true}, "csrf-state", "verifier-value")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	q := parsed.Query()
	wantChallengeBytes := sha256.Sum256([]byte("verifier-value"))
	wantChallenge := base64.RawURLEncoding.EncodeToString(wantChallengeBytes[:])
	if q.Get("state") != "csrf-state" || q.Get("scope") != "user:read events:subscribe" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != wantChallenge {
		t.Fatalf("bad OAuth URL: %s", u)
	}
	if strings.Contains(u, "access_token") || strings.Contains(u, "client_secret") {
		t.Fatal("secret in authorization URL")
	}
}

func TestTwitchReadOnlyAuthorizationScope(t *testing.T) {
	u, err := AuthorizationURL(Request{AuthorizeURL: "https://id.twitch.tv/oauth2/authorize", ClientID: "client", RedirectURI: "http://localhost:8790/oauth/twitch/callback", Scopes: []string{"user:read:chat"}}, "state", "")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	scope := q.Query().Get("scope")
	if scope != "user:read:chat" || strings.Contains(scope, "write") || strings.Contains(scope, "moderator") {
		t.Fatalf("scope %q", scope)
	}
}

func TestAuthorizationURLIncludesOfflineParametersWithoutSecrets(t *testing.T) {
	u, err := AuthorizationURL(Request{AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", ClientID: "client", RedirectURI: "http://localhost:8791/oauth/youtube/callback", Scopes: []string{"https://www.googleapis.com/auth/youtube.readonly"}, UsePKCE: true, Parameters: map[string]string{"access_type": "offline", "include_granted_scopes": "true", "prompt": "consent"}}, "state", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	values := q.Query()
	if values.Get("access_type") != "offline" || values.Get("prompt") != "consent" || values.Get("scope") != "https://www.googleapis.com/auth/youtube.readonly" {
		t.Fatal(u)
	}
	if strings.Contains(u, "client_secret") || strings.Contains(u, "refresh_token") {
		t.Fatalf("secret in authorization URL: %s", u)
	}
}
