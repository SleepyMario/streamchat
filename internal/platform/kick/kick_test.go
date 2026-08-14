package kick

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func fixture(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	p, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return p, &p.PublicKey
}
func sign(t *testing.T, p *rsa.PrivateKey, id, stamp string, b []byte) string {
	t.Helper()
	h := sha256.Sum256([]byte(id + "." + stamp + "." + string(b)))
	s, e := rsa.SignPKCS1v15(rand.Reader, p, crypto.SHA256, h[:])
	if e != nil {
		t.Fatal(e)
	}
	return base64.StdEncoding.EncodeToString(s)
}

const body = `{"message_id":"m1","replies_to":{"message_id":"old","content":"parent","sender":{"user_id":8,"username":"parent"}},"broadcaster":{"user_id":1,"username":"channel","channel_slug":"chan"},"sender":{"user_id":2,"username":"viewer","identity":{"username_color":"#fff","badges":[{"text":"Moderator","type":"moderator","count":1}]}},"content":"hi [emote:7:WAVE]","emotes":[{"emote_id":"7","positions":[{"s":3,"e":16}]}],"created_at":"2026-01-01T00:00:00Z"}`

func request(t *testing.T, s *Server, p *rsa.PrivateKey, b []byte, stamp string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/kick", bytes.NewReader(b))
	id := "01TEST"
	r.Header.Set("Kick-Event-Message-Id", id)
	r.Header.Set("Kick-Event-Subscription-Id", "01SUB")
	r.Header.Set("Kick-Event-Signature", sign(t, p, id, stamp, b))
	r.Header.Set("Kick-Event-Message-Timestamp", stamp)
	r.Header.Set("Kick-Event-Type", "chat.message.sent")
	r.Header.Set("Kick-Event-Version", "1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSignatureParsingReplyBadgeEmoteAndDedup(t *testing.T) {
	p, k := fixture(t)
	out := make(chan chat.Message, 2)
	now := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	s := NewServer("127.0.0.1:0", k, out)
	s.Now = func() time.Time { return now }
	stamp := now.Format(time.RFC3339)
	if w := request(t, s, p, []byte(body), stamp); w.Code != 204 {
		t.Fatalf("%d", w.Code)
	}
	m := <-out
	if m.Reply == nil || len(m.Badges) != 1 || len(m.Emotes) != 1 || m.AuthorColor != "#fff" {
		t.Fatalf("%+v", m)
	}
	if w := request(t, s, p, []byte(body), stamp); w.Code != 204 {
		t.Fatal(w.Code)
	}
	select {
	case <-out:
		t.Fatal("duplicate emitted")
	default:
	}
}
func TestRejectsInvalidStaleOversizeMethodAndHealth(t *testing.T) {
	p, k := fixture(t)
	s := NewServer("127.0.0.1:0", k, make(chan chat.Message, 1))
	now := time.Now()
	s.Now = func() time.Time { return now }
	w := request(t, s, p, []byte(body), now.Add(-time.Hour).Format(time.RFC3339))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	s.MaxBody = 4
	w = request(t, s, p, []byte(body), now.Format(time.RFC3339))
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/webhooks/kick", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "key") {
		t.Fatal(w.Body.String())
	}
}
func TestInvalidSignatureAndHeaders(t *testing.T) {
	_, k := fixture(t)
	s := NewServer("x", k, make(chan chat.Message, 1))
	r := httptest.NewRequest(http.MethodPost, "/webhooks/kick", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/webhooks/kick", strings.NewReader(body))
	for h, v := range map[string]string{"Kick-Event-Message-Id": "1", "Kick-Event-Subscription-Id": "2", "Kick-Event-Signature": "bad", "Kick-Event-Message-Timestamp": time.Now().Format(time.RFC3339), "Kick-Event-Type": "chat.message.sent", "Kick-Event-Version": "1"} {
		r.Header.Set(h, v)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestParsePublicKey(t *testing.T) {
	p, _ := fixture(t)
	der, _ := x509.MarshalPKIXPublicKey(&p.PublicKey)
	b := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if _, e := ParsePublicKey(b); e != nil {
		t.Fatal(e)
	}
}
func TestGracefulShutdown(t *testing.T) {
	_, k := fixture(t)
	s := NewServer("127.0.0.1:0", k, make(chan chat.Message, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.Run(ctx, nil); e != nil {
		t.Fatal(e)
	}
}

func TestOAuthExchangeSuccess(t *testing.T) {
	const (
		code     = "authorization-code"
		verifier = "pkce-verifier"
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("unexpected request: %s %q", r.Method, r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		want := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {"http://localhost:8789/oauth/kick/callback"},
			"code_verifier": {verifier},
			"client_id":     {"client"},
			"client_secret": {"secret"},
		}
		if form.Encode() != want.Encode() {
			t.Fatalf("token form mismatch: got %q want %q", form.Encode(), want.Encode())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":"user:read events:subscribe"}`)
	}))
	defer s.Close()
	c := OAuthClient{HTTP: s.Client(), OAuthBaseURL: s.URL, ClientID: " client\n", ClientSecret: " secret\r\n"}
	tok, err := c.Exchange(context.Background(), code, "http://localhost:8789/oauth/kick/callback", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh" || tok.ExpiresIn != 3600 {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestOAuthErrorBodyIsUsefulAndSecretsAreRedacted(t *testing.T) {
	const (
		clientSecret = "client-secret-value"
		code         = "authorization-code-value"
		verifier     = "pkce-verifier-value"
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"client authentication failed for client-secret-value; code authorization-code-value; verifier pkce-verifier-value; tokens must-not-appear and must-not-appear-either","access_token":"must-not-appear","refresh_token":"must-not-appear-either"}`)
	}))
	defer s.Close()
	c := OAuthClient{HTTP: s.Client(), OAuthBaseURL: s.URL, ClientID: "client", ClientSecret: clientSecret}
	_, err := c.Exchange(context.Background(), code, "http://localhost:8789/oauth/kick/callback", verifier)
	if err == nil {
		t.Fatal("invalid credential accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "HTTP 401: invalid_client: client authentication failed") {
		t.Fatalf("provider error was not reported: %q", message)
	}
	for _, secret := range []string{clientSecret, code, verifier, "must-not-appear", "must-not-appear-either"} {
		if strings.Contains(message, secret) {
			t.Fatalf("secret %q leaked in %q", secret, message)
		}
	}
}

func TestOAuthEmptyUnauthorizedIdentifiesClientAuthentication(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer s.Close()
	c := OAuthClient{HTTP: s.Client(), OAuthBaseURL: s.URL, ClientID: "client", ClientSecret: "client-secret"}
	_, err := c.Exchange(context.Background(), "authorization-code", "http://localhost:8789/oauth/kick/callback", "pkce-verifier")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401: client authentication rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, secret := range []string{"client-secret", "authorization-code", "pkce-verifier"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked in %q", secret, err)
		}
	}
}
