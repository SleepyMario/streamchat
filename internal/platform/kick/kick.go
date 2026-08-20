package kick

import (
	"bytes"
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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SleepyMario/streamchat/internal/chat"
)

var (
	ErrChatAuthentication  = errors.New("Kick chat authentication failed")
	ErrChatWritePermission = errors.New("Kick chat-write permission is missing; reauthorize Kick with chat:write")
	ErrChatRateLimit       = errors.New("Kick chat API rate limit exceeded; try again later")
)

const ChatWriteScope = "chat:write"

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
	Broadcaster user         `json:"broadcaster"`
	Sender      user         `json:"sender"`
	Content     string       `json:"content"`
	Emotes      []eventEmote `json:"emotes"`
	CreatedAt   string       `json:"created_at"`
}

type eventEmote struct {
	EmoteID   string               `json:"emote_id"`
	Positions []struct{ S, E int } `json:"positions"`
}

var kickEmoteToken = regexp.MustCompile(`\[emote:([0-9]+):([^\]\r\n]+)\]`)

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
	if v.Sender.UserID > 0 && v.Sender.UserID == v.Broadcaster.UserID {
		m.Roles.Add(chat.RoleBroadcaster)
	}
	if v.Sender.Identity != nil {
		m.AuthorColor = v.Sender.Identity.UsernameColor
		for _, b := range v.Sender.Identity.Badges {
			m.Badges = append(m.Badges, chat.Badge{Type: b.Type, Text: b.Text, Count: b.Count})
			switch strings.ToLower(strings.TrimSpace(b.Type)) {
			case "broadcaster":
				m.Roles.Add(chat.RoleBroadcaster)
			case "moderator":
				m.Roles.Add(chat.RoleModerator)
			case "partner":
				m.Roles.Add(chat.RolePartner)
			case "vip":
				m.Roles.Add(chat.RoleVIP)
			case "og":
				m.Roles.Add(chat.RoleOG)
			case "subscriber":
				m.Roles.Add(chat.RoleSubscriber)
			}
		}
	}
	m.Emotes = normalizeKickEmotes(v.Content, v.Emotes)
	if v.RepliesTo != nil {
		m.Reply = &chat.Reply{MessageID: v.RepliesTo.MessageID, AuthorID: strconv.FormatInt(v.RepliesTo.Sender.UserID, 10), AuthorDisplayName: v.RepliesTo.Sender.Username, Text: v.RepliesTo.Content}
	}
	return m, nil
}

func normalizeKickEmotes(content string, provider []eventEmote) []chat.Emote {
	emotes := make([]chat.Emote, 0)
	for _, emote := range provider {
		if len(emote.Positions) == 0 {
			emotes = append(emotes, chat.Emote{ID: emote.EmoteID, Start: -1, End: -1})
			continue
		}
		for _, position := range emote.Positions {
			emotes = append(emotes, chat.Emote{ID: emote.EmoteID, Start: position.S, End: position.E})
		}
	}
	return EnrichEmotes(content, emotes)
}

// EnrichEmotes fills the name, canonical asset URL, and exact rune range from
// Kick's structured IDs plus content tokens. It is also applied by interactive
// clients so messages from older relay servers remain renderable.
func EnrichEmotes(content string, structured []chat.Emote) []chat.Emote {
	type token struct {
		id, name   string
		start, end int
		used       bool
	}
	tokens := make([]token, 0)
	for _, match := range kickEmoteToken.FindAllStringSubmatchIndex(content, -1) {
		start := utf8.RuneCountInString(content[:match[0]])
		tokens = append(tokens, token{
			id:    content[match[2]:match[3]],
			name:  content[match[4]:match[5]],
			start: start,
			end:   start + utf8.RuneCountInString(content[match[0]:match[1]]) - 1,
		})
	}
	emotes := append([]chat.Emote(nil), structured...)
	for index := range emotes {
		item := &emotes[index]
		if item.URL == "" {
			item.URL = kickEmoteURL(item.ID)
		}
		matchIndex := -1
		for candidate := range tokens {
			if tokens[candidate].used || tokens[candidate].id != item.ID {
				continue
			}
			if tokens[candidate].start == item.Start && tokens[candidate].end == item.End {
				matchIndex = candidate
				break
			}
			if matchIndex == -1 {
				matchIndex = candidate
			}
		}
		if matchIndex < 0 {
			continue
		}
		matched := &tokens[matchIndex]
		matched.used = true
		item.Start = matched.start
		item.End = matched.end
		if item.Name == "" {
			item.Name = matched.name
		}
	}
	return emotes
}

func kickEmoteURL(id string) string {
	if id == "" {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "https://files.kick.com/emotes/" + id + "/fullsize"
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
	m, e := Parse(body, id)
	if e != nil {
		http.Error(w, "invalid webhook", 400)
		return
	}
	s.mu.Lock()
	if _, ok := s.seen[id]; ok {
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	select {
	case s.Out <- m:
		now := s.Now()
		s.seen[id] = now
		for k, t := range s.seen {
			if now.Sub(t) > s.MaxAge*2 {
				delete(s.seen, k)
			}
		}
		s.mu.Unlock()
		w.WriteHeader(204)
	default:
		s.mu.Unlock()
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

type ChatClient struct {
	HTTP                 *http.Client
	BaseURL, AccessToken string
}

func (c ChatClient) Send(ctx context.Context, broadcaster, message string) error {
	_, err := c.SendMessage(ctx, broadcaster, message)
	return err
}

type ChatReceipt struct {
	MessageID string
}

func (c ChatClient) SendMessage(ctx context.Context, broadcaster, message string) (ChatReceipt, error) {
	if c.AccessToken == "" {
		return ChatReceipt{}, fmt.Errorf("%w; run: streamchat setup kick", ErrChatAuthentication)
	}
	broadcasterID, err := strconv.ParseInt(broadcaster, 10, 64)
	if err != nil || broadcasterID <= 0 {
		return ChatReceipt{}, errors.New("Kick broadcaster user ID is missing; run: streamchat setup kick")
	}
	if strings.TrimSpace(message) == "" {
		return ChatReceipt{}, errors.New("Kick chat message cannot be empty")
	}
	if utf8.RuneCountInString(message) > 500 {
		return ChatReceipt{}, errors.New("Kick chat messages are limited to 500 characters")
	}
	body, err := json.Marshal(struct {
		BroadcasterUserID int64  `json:"broadcaster_user_id"`
		Content           string `json:"content"`
		Type              string `json:"type"`
	}{BroadcasterUserID: broadcasterID, Content: message, Type: "user"})
	if err != nil {
		return ChatReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat", bytes.NewReader(body))
	if err != nil {
		return ChatReceipt{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	r, err := httpClient.Do(req)
	if err != nil {
		return ChatReceipt{}, fmt.Errorf("Kick chat request failed: %w", err)
	}
	defer r.Body.Close()
	switch r.StatusCode {
	case http.StatusOK:
		var response struct {
			Data struct {
				IsSent    bool   `json:"is_sent"`
				MessageID string `json:"message_id"`
			} `json:"data"`
		}
		if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&response); err != nil || !response.Data.IsSent || strings.TrimSpace(response.Data.MessageID) == "" {
			return ChatReceipt{}, errors.New("Kick chat returned an invalid send receipt")
		}
		return ChatReceipt{MessageID: response.Data.MessageID}, nil
	case http.StatusUnauthorized:
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return ChatReceipt{}, fmt.Errorf("%w (HTTP 401); run: streamchat setup kick", ErrChatAuthentication)
	case http.StatusForbidden:
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return ChatReceipt{}, fmt.Errorf("%w (HTTP 403); run: streamchat setup kick", ErrChatWritePermission)
	case http.StatusTooManyRequests:
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return ChatReceipt{}, fmt.Errorf("%w (HTTP 429)", ErrChatRateLimit)
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return ChatReceipt{}, fmt.Errorf("Kick chat API failed (HTTP %d)", r.StatusCode)
	}
}

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type OAuthClient struct {
	HTTP                     *http.Client
	OAuthBaseURL, APIBaseURL string
	ClientID, ClientSecret   string
}

func (c OAuthClient) token(ctx context.Context, form url.Values) (Token, error) {
	// Kick-generated client credentials do not contain surrounding whitespace.
	// Normalize values loaded from hand-edited config files or environment
	// variables so an invisible newline cannot turn valid credentials into a
	// misleading invalid-client response.
	form.Set("client_id", strings.TrimSpace(c.ClientID))
	form.Set("client_secret", strings.TrimSpace(c.ClientSecret))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.OAuthBaseURL, "/")+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := c.HTTP.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if readErr != nil {
			return Token{}, fmt.Errorf("Kick authorization failed (HTTP %d; error response could not be read); run: streamchat setup kick", r.StatusCode)
		}
		detail := oauthErrorDetail(body, sensitiveFormValues(form)...)
		if detail != "" {
			return Token{}, fmt.Errorf("Kick authorization failed (HTTP %d: %s); run: streamchat setup kick", r.StatusCode, detail)
		}
		if r.StatusCode == http.StatusUnauthorized {
			return Token{}, fmt.Errorf("Kick authorization failed (HTTP %d: client authentication rejected); run: streamchat setup kick", r.StatusCode)
		}
		return Token{}, fmt.Errorf("Kick authorization failed (HTTP %d); run: streamchat setup kick", r.StatusCode)
	}
	var tok Token
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&tok); err != nil {
		return Token{}, err
	}
	if tok.AccessToken == "" {
		return Token{}, errors.New("Kick authorization returned no access token")
	}
	return tok, nil
}

// oauthErrorDetail returns only the standard, useful OAuth error fields. It
// deliberately does not include the raw response body, which may contain token
// fields or reflected request data.
func oauthErrorDetail(body []byte, secrets ...string) string {
	var response struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if response.Error != "" {
		parts = append(parts, response.Error)
	}
	description := response.ErrorDescription
	if description == "" {
		description = response.Message
	}
	if description != "" && description != response.Error {
		parts = append(parts, description)
	}
	detail := strings.Join(parts, ": ")
	secrets = append(secrets, response.AccessToken, response.RefreshToken)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "<redacted>")
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 512 {
		detail = detail[:512] + "…"
	}
	return detail
}

func sensitiveFormValues(form url.Values) []string {
	return []string{
		form.Get("client_secret"),
		form.Get("code"),
		form.Get("code_verifier"),
		form.Get("refresh_token"),
		form.Get("access_token"),
	}
}

func (c OAuthClient) Exchange(ctx context.Context, code, redirectURI, verifier string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
}

func (c OAuthClient) Refresh(ctx context.Context, refresh string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

func (c OAuthClient) CurrentUser(ctx context.Context, access string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.APIBaseURL, "/")+"/users", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	r, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("Kick user lookup failed (HTTP %d); authorization needs user:read", r.StatusCode)
	}
	var v struct {
		Data []struct {
			UserID int64  `json:"user_id"`
			Name   string `json:"name"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		return "", "", err
	}
	if len(v.Data) == 0 || v.Data[0].UserID <= 0 {
		return "", "", errors.New("Kick did not return the authorized user")
	}
	return strconv.FormatInt(v.Data[0].UserID, 10), v.Data[0].Name, nil
}

func (c SubscriptionClient) Do(ctx context.Context, method, broadcaster string) ([]byte, error) {
	if c.AccessToken == "" {
		return nil, errors.New("Kick OAuth access token with events:subscribe scope is required")
	}
	if n, err := strconv.ParseInt(broadcaster, 10, 64); err != nil || n <= 0 {
		return nil, errors.New("Kick broadcaster user ID must be a positive integer")
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
