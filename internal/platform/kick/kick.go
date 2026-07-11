package kick

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/SleepyMario/streamchat/internal/chat"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const OfficialPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAq/+l1WnlRrGSolDMA+A8
6rAhMbQGmQ2SapVcGM3zq8ANXjnhDWocMqfWcTd95btDydITa10kDvHzw9WQOqp2
MZI7ZyrfzJuz5nhTPCiJwTwnEtWft7nV14BYRDHvlfqPUaZ+1KR4OCaO/wWIk/rQ
L/TjY0M70gse8rlBkbo2a8rKhu69RQTRsoaf4DVhDPEeSeI5jVrRDGAMGL3cGuyY
6CLKGdjVEM78g3JfYOvDU/RvfqD7L89TZ3iN94jrmWdGz34JNlEI5hqK8dd7C5EF
BEbZ5jgB8s8ReQV8H+MkuffjdAj3ajDDX3DOJMIut1lBrUVD1AaSrGCKHooWoL2e
twIDAQAB
-----END PUBLIC KEY-----`

func ParsePublicKey(p []byte) (*rsa.PublicKey, error) {
	b, _ := pem.Decode(p)
	if b == nil || b.Type != "PUBLIC KEY" {
		return nil, errors.New("invalid Kick public key PEM")
	}
	k, e := x509.ParsePKIXPublicKey(b.Bytes)
	if e != nil {
		return nil, e
	}
	r, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("Kick public key is not RSA")
	}
	return r, nil
}
func Verify(k *rsa.PublicKey, id, ts string, body []byte, sig string) error {
	raw, e := base64.StdEncoding.DecodeString(sig)
	if e != nil {
		return errors.New("invalid signature encoding")
	}
	sum := sha256.Sum256([]byte(id + "." + ts + "." + string(body)))
	if e = rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], raw); e != nil {
		return errors.New("invalid Kick signature")
	}
	return nil
}

type user struct {
	IsAnonymous bool   `json:"is_anonymous"`
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	ChannelSlug string `json:"channel_slug"`
	Identity    *struct {
		UsernameColor string `json:"username_color"`
		Badges        []struct {
			Text, Type string
			Count      int
		} `json:"badges"`
	} `json:"identity"`
}
type event struct {
	MessageID string `json:"message_id"`
	RepliesTo *struct {
		MessageID, Content string
		Sender             user
	} `json:"replies_to"`
	Broadcaster user   `json:"broadcaster"`
	Sender      user   `json:"sender"`
	Content     string `json:"content"`
	Emotes      []struct {
		EmoteID   string               `json:"emote_id"`
		Positions []struct{ S, E int } `json:"positions"`
	} `json:"emotes"`
	CreatedAt string `json:"created_at"`
}

func Parse(body []byte, eventID string) (chat.Message, error) {
	var v event
	if e := json.Unmarshal(body, &v); e != nil {
		return chat.Message{}, e
	}
	if v.MessageID == "" {
		return chat.Message{}, errors.New("Kick message_id is required")
	}
	ts, e := time.Parse(time.RFC3339Nano, v.CreatedAt)
	if e != nil {
		return chat.Message{}, errors.New("invalid Kick created_at")
	}
	m := chat.Message{ID: v.MessageID, Platform: chat.PlatformKick, ChannelID: strconv.FormatInt(v.Broadcaster.UserID, 10), ChannelDisplayName: v.Broadcaster.Username, Timestamp: ts, AuthorID: strconv.FormatInt(v.Sender.UserID, 10), AuthorDisplayName: v.Sender.Username, Text: v.Content, EventType: chat.EventMessage, SafePlatformMetadata: map[string]string{"kick_event_id": eventID, "channel_slug": v.Broadcaster.ChannelSlug}}
	if v.Sender.Identity != nil {
		m.AuthorColor = v.Sender.Identity.UsernameColor
		for _, b := range v.Sender.Identity.Badges {
			m.Badges = append(m.Badges, chat.Badge{Type: b.Type, Text: b.Text, Count: b.Count})
			m.Roles = append(m.Roles, b.Type)
		}
	}
	for _, e := range v.Emotes {
		if len(e.Positions) == 0 {
			m.Emotes = append(m.Emotes, chat.Emote{ID: e.EmoteID})
		}
		for _, p := range e.Positions {
			m.Emotes = append(m.Emotes, chat.Emote{ID: e.EmoteID, Start: p.S, End: p.E})
		}
	}
	if v.RepliesTo != nil {
		m.Reply = &chat.Reply{MessageID: v.RepliesTo.MessageID, AuthorID: strconv.FormatInt(v.RepliesTo.Sender.UserID, 10), AuthorDisplayName: v.RepliesTo.Sender.Username, Text: v.RepliesTo.Content}
	}
	return m, nil
}

type Server struct {
	Listen  string
	Key     *rsa.PublicKey
	Out     chan<- chat.Message
	MaxBody int64
	MaxAge  time.Duration
	Now     func() time.Time
	mu      sync.Mutex
	seen    map[string]time.Time
	server  *http.Server
}

func NewServer(listen string, key *rsa.PublicKey, out chan<- chat.Message) *Server {
	return &Server{Listen: listen, Key: key, Out: out, MaxBody: 1 << 20, MaxAge: 5 * time.Minute, Now: time.Now, seen: map[string]time.Time{}}
}
func (s *Server) Name() string { return "kick" }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"status\":\"ok\"}\n")
	})
	mux.HandleFunc("/webhooks/kick", s.webhook)
	return mux
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := r.Header.Get("Kick-Event-Message-Id")
	sub := r.Header.Get("Kick-Event-Subscription-Id")
	sig := r.Header.Get("Kick-Event-Signature")
	stamp := r.Header.Get("Kick-Event-Message-Timestamp")
	typ := r.Header.Get("Kick-Event-Type")
	ver := r.Header.Get("Kick-Event-Version")
	if id == "" || sub == "" || sig == "" || stamp == "" {
		http.Error(w, "invalid webhook", 400)
		return
	}
	if typ != "chat.message.sent" || ver != "1" {
		http.Error(w, "unsupported event", 400)
		return
	}
	ts, e := time.Parse(time.RFC3339Nano, stamp)
	if e != nil || dabs(s.Now().Sub(ts)) > s.MaxAge {
		http.Error(w, "invalid webhook", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBody)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		http.Error(w, "request too large", 413)
		return
	}
	if e = Verify(s.Key, id, stamp, body, sig); e != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	s.mu.Lock()
	if _, ok := s.seen[id]; ok {
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	s.seen[id] = s.Now()
	for k, t := range s.seen {
		if s.Now().Sub(t) > s.MaxAge*2 {
			delete(s.seen, k)
		}
	}
	s.mu.Unlock()
	m, e := Parse(body, id)
	if e != nil {
		http.Error(w, "invalid webhook", 400)
		return
	}
	select {
	case s.Out <- m:
		w.WriteHeader(204)
	default:
		http.Error(w, "busy", 503)
	}
}
func dabs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
func (s *Server) Run(ctx context.Context, out chan<- chat.Message) error {
	if out != nil {
		s.Out = out
	}
	s.server = &http.Server{Addr: s.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errc := make(chan error, 1)
	go func() { errc <- s.server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(c)
		return nil
	case e := <-errc:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}

type SubscriptionClient struct {
	HTTP                 *http.Client
	BaseURL, AccessToken string
}

func (c SubscriptionClient) Do(ctx context.Context, method, broadcaster string) ([]byte, error) {
	if c.AccessToken == "" {
		return nil, errors.New("Kick OAuth access token with events:subscribe scope is required")
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(fmt.Sprintf(`{"broadcaster_user_id":%s,"events":[{"name":"chat.message.sent","version":1}],"method":"webhook"}`, broadcaster))
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/events/subscriptions"
	if method == http.MethodGet && broadcaster != "" {
		u += "?broadcaster_user_id=" + broadcaster
	}
	req, e := http.NewRequestWithContext(ctx, method, u, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, e := c.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if e != nil {
		return nil, e
	}
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Kick subscriptions API returned HTTP %d", r.StatusCode)
	}
	return b, nil
}
