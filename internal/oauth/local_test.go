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
