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
	"github.com/SleepyMario/streamchat/internal/chat"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
