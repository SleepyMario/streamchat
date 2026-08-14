package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/gorilla/websocket"
)

const ReadChatScope = "user:read:chat"
const EventType = "channel.chat.message"

type Token struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scopes       []string `json:"scope"`
}

type API struct {
	HTTP                                *http.Client
	APIBaseURL, OAuthBaseURL            string
	ClientID, ClientSecret, AccessToken string
	RefreshToken                        string
	OnToken                             func(Token) error
}

func (a *API) token(ctx context.Context, form url.Values) (Token, error) {
	form.Set("client_id", a.ClientID)
	form.Set("client_secret", a.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.OAuthBaseURL, "/")+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := a.HTTP.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return Token{}, fmt.Errorf("Twitch authorization failed (HTTP %d); run: streamchat setup twitch", r.StatusCode)
	}
	var tok Token
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&tok); err != nil {
		return Token{}, err
	}
	if tok.AccessToken == "" {
		return Token{}, errors.New("Twitch authorization returned no access token")
	}
	return tok, nil
}

func (a *API) Exchange(ctx context.Context, code, redirectURI string) (Token, error) {
	return a.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}})
}

func (a *API) Refresh(ctx context.Context, refresh string) (Token, error) {
	return a.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

type Identity struct {
	ClientID, UserID, Login string
	Scopes                  []string
	ExpiresIn               int
}

func (a *API) ValidateToken(ctx context.Context) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.OAuthBaseURL, "/")+"/validate", nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "OAuth "+a.AccessToken)
	r, err := a.HTTP.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return Identity{}, &chat.AdapterError{Kind: chat.Authentication, Op: "Twitch token validation", Err: errors.New("authorization is invalid or expired; run: streamchat setup twitch")}
	}
	var v struct {
		ClientID  string   `json:"client_id"`
		UserID    string   `json:"user_id"`
		Login     string   `json:"login"`
		Scopes    []string `json:"scopes"`
		ExpiresIn int      `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		return Identity{}, err
	}
	if v.ClientID != a.ClientID {
		return Identity{}, errors.New("Twitch access token belongs to a different Client ID; run: streamchat setup twitch")
	}
	if !contains(v.Scopes, ReadChatScope) {
		return Identity{}, errors.New("Twitch authorization lacks user:read:chat; run: streamchat setup twitch")
	}
	return Identity{v.ClientID, v.UserID, v.Login, v.Scopes, v.ExpiresIn}, nil
}

type User struct{ ID, Login, DisplayName string }

func (a *API) User(ctx context.Context, login string) (User, error) {
	u := strings.TrimRight(a.APIBaseURL, "/") + "/users?login=" + url.QueryEscape(login)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Client-Id", a.ClientID)
	r, err := a.HTTP.Do(req)
	if err != nil {
		return User{}, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusUnauthorized {
		return User{}, &chat.AdapterError{Kind: chat.Authentication, Op: "Twitch user lookup", Err: errors.New("authorization expired; run: streamchat setup twitch")}
	}
	if r.StatusCode/100 != 2 {
		return User{}, fmt.Errorf("Twitch channel lookup returned HTTP %d", r.StatusCode)
	}
	var v struct {
		Data []struct{ ID, Login, DisplayName string } `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		return User{}, err
	}
	if len(v.Data) == 0 {
		return User{}, fmt.Errorf("Twitch channel %q was not found", login)
	}
	return User(v.Data[0]), nil
}

func (a *API) Subscribe(ctx context.Context, session, broadcaster, user string) error {
	body, _ := json.Marshal(map[string]any{"type": EventType, "version": "1", "condition": map[string]string{"broadcaster_user_id": broadcaster, "user_id": user}, "transport": map[string]string{"method": "websocket", "session_id": session}})
	status, err := a.subscribe(ctx, body)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && a.RefreshToken != "" {
		tok, e := a.Refresh(ctx, a.RefreshToken)
		if e != nil {
			return e
		}
		a.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			a.RefreshToken = tok.RefreshToken
		}
		if a.OnToken != nil {
			if e = a.OnToken(tok); e != nil {
				return e
			}
		}
		status, err = a.subscribe(ctx, body)
		if err != nil {
			return err
		}
	}
	if status/100 != 2 {
		kind := chat.Terminal
		if status == http.StatusUnauthorized {
			kind = chat.Authentication
		}
		return &chat.AdapterError{Kind: kind, Op: "Twitch chat subscription", Err: fmt.Errorf("HTTP %d; verify authorization/channel and run: streamchat setup twitch", status)}
	}
	return nil
}

func (a *API) subscribe(ctx context.Context, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.APIBaseURL, "/")+"/eventsub/subscriptions", strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Client-Id", a.ClientID)
	req.Header.Set("Content-Type", "application/json")
	r, err := a.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, nil
}

func ParseChannel(input string) (string, error) {
	s := strings.TrimSpace(input)
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", errors.New("invalid Twitch channel URL")
		}
		h := strings.ToLower(u.Hostname())
		if h != "twitch.tv" && h != "www.twitch.tv" {
			return "", errors.New("Twitch channel URL must use twitch.tv")
		}
		s = strings.Trim(strings.Split(strings.Trim(u.Path, "/"), "/")[0], " ")
	}
	s = strings.TrimPrefix(s, "@")
	s = strings.ToLower(s)
	if s == "" || strings.ContainsAny(s, "/?# ") {
		return "", errors.New("enter a Twitch channel name or URL")
	}
	return s, nil
}

type wsConn interface {
	ReadMessage() (int, []byte, error)
	Close() error
	SetReadDeadline(time.Time) error
}
type Dial func(context.Context, string) (wsConn, error)

func defaultDial(ctx context.Context, u string) (wsConn, error) {
	c, _, err := websocket.DefaultDialer.DialContext(ctx, u, nil)
	return c, err
}

type Client struct {
	API                           *API
	WebSocketURL, Channel, UserID string
	Dial                          Dial
	mu                            sync.Mutex
	seen                          map[string]struct{}
	order                         []string
}

func New(api *API, wsURL, channel, userID string) *Client {
	return &Client{API: api, WebSocketURL: wsURL, Channel: channel, UserID: userID, Dial: defaultDial, seen: map[string]struct{}{}}
}
func (c *Client) Name() string { return "twitch" }

type envelope struct {
	Metadata struct {
		MessageID        string `json:"message_id"`
		MessageType      string `json:"message_type"`
		MessageTimestamp string `json:"message_timestamp"`
		SubscriptionType string `json:"subscription_type"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID           string `json:"id"`
			Keepalive    int    `json:"keepalive_timeout_seconds"`
			ReconnectURL string `json:"reconnect_url"`
		} `json:"session"`
		Subscription struct {
			Status string `json:"status"`
		} `json:"subscription"`
		Event channelEvent `json:"event"`
	} `json:"payload"`
}

type channelEvent struct {
	BroadcasterID, BroadcasterName, BroadcasterLogin string
	ChatterID, ChatterName, ChatterLogin             string
	MessageID                                        string `json:"message_id"`
	Color                                            string `json:"color"`
	Badges                                           []struct {
		SetID string `json:"set_id"`
		ID    string `json:"id"`
		Info  string `json:"info"`
	} `json:"badges"`
	Message struct {
		Text      string `json:"text"`
		Fragments []struct {
			Type, Text string
			Emote      *struct {
				ID, OwnerID string
				Format      []string `json:"format"`
			} `json:"emote"`
		} `json:"fragments"`
	} `json:"message"`
}

func (e *channelEvent) UnmarshalJSON(b []byte) error {
	type alias channelEvent
	var v struct {
		alias
		BroadcasterID    string `json:"broadcaster_user_id"`
		BroadcasterName  string `json:"broadcaster_user_name"`
		BroadcasterLogin string `json:"broadcaster_user_login"`
		ChatterID        string `json:"chatter_user_id"`
		ChatterName      string `json:"chatter_user_name"`
		ChatterLogin     string `json:"chatter_user_login"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*e = channelEvent(v.alias)
	e.BroadcasterID = v.BroadcasterID
	e.BroadcasterName = v.BroadcasterName
	e.BroadcasterLogin = v.BroadcasterLogin
	e.ChatterID = v.ChatterID
	e.ChatterName = v.ChatterName
	e.ChatterLogin = v.ChatterLogin
	return nil
}

func ParseEvent(b []byte) (envelope, *chat.Message, error) {
	var v envelope
	if err := json.Unmarshal(b, &v); err != nil {
		return v, nil, err
	}
	if v.Metadata.MessageType != "notification" {
		return v, nil, nil
	}
	if v.Metadata.SubscriptionType != EventType {
		return v, nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, v.Metadata.MessageTimestamp)
	if err != nil {
		return v, nil, err
	}
	e := v.Payload.Event
	m := chat.Message{ID: e.MessageID, Platform: chat.PlatformTwitch, ChannelID: e.BroadcasterID, ChannelDisplayName: e.BroadcasterName, Timestamp: ts, AuthorID: e.ChatterID, AuthorDisplayName: e.ChatterName, AuthorColor: e.Color, Text: e.Message.Text, EventType: chat.EventMessage, SafePlatformMetadata: map[string]string{"twitch_login": e.ChatterLogin}}
	pos := 0
	for _, f := range e.Message.Fragments {
		if f.Emote != nil {
			m.Emotes = append(m.Emotes, chat.Emote{ID: f.Emote.ID, Name: f.Text, Start: pos, End: pos + len([]rune(f.Text))})
		}
		pos += len([]rune(f.Text))
	}
	for _, b := range e.Badges {
		m.Badges = append(m.Badges, chat.Badge{Type: b.SetID})
		m.Roles = append(m.Roles, b.SetID)
	}
	if err = m.Validate(); err != nil {
		return v, nil, err
	}
	return v, &m, nil
}

func (c *Client) duplicate(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[id]; ok {
		return true
	}
	c.seen[id] = struct{}{}
	c.order = append(c.order, id)
	if len(c.order) > 10000 {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	return false
}

func (c *Client) runSocket(ctx context.Context, conn wsConn, broadcaster string, inherited bool, out chan<- chat.Message) (string, error) {
	welcomed := false
	keepalive := 10 * time.Second
	for {
		_, b, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", err
		}
		v, m, err := ParseEvent(b)
		if err != nil {
			return "", err
		}
		if welcomed {
			_ = conn.SetReadDeadline(time.Now().Add(keepalive + 5*time.Second))
		}
		switch v.Metadata.MessageType {
		case "session_welcome":
			welcomed = true
			timeout := v.Payload.Session.Keepalive
			if timeout <= 0 {
				timeout = 10
			}
			keepalive = time.Duration(timeout) * time.Second
			_ = conn.SetReadDeadline(time.Now().Add(keepalive + 5*time.Second))
			if !inherited {
				if err = c.API.Subscribe(ctx, v.Payload.Session.ID, broadcaster, c.UserID); err != nil {
					return "", err
				}
			}
		case "session_keepalive":
		case "session_reconnect":
			if v.Payload.Session.ReconnectURL == "" {
				return "", errors.New("Twitch reconnect message lacked a URL")
			}
			return v.Payload.Session.ReconnectURL, nil
		case "revocation":
			kind := chat.Terminal
			if v.Payload.Subscription.Status == "authorization_revoked" {
				kind = chat.Authentication
			}
			return "", &chat.AdapterError{Kind: kind, Op: "Twitch EventSub", Err: fmt.Errorf("subscription revoked (%s); run: streamchat setup twitch", v.Payload.Subscription.Status)}
		case "notification":
			if m != nil && !c.duplicate(v.Metadata.MessageID) {
				select {
				case out <- *m:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
		}
	}
}

func (c *Client) Run(ctx context.Context, out chan<- chat.Message) error {
	login, err := ParseChannel(c.Channel)
	if err != nil {
		return err
	}
	user, err := c.API.User(ctx, login)
	if err != nil {
		return err
	}
	u := c.WebSocketURL
	inherited := false
	backoff := time.Second
	for {
		conn, err := c.Dial(ctx, u)
		if err != nil {
			if !wait(ctx, backoff) {
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			u = c.WebSocketURL
			inherited = false
			continue
		}
		closed := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-closed:
			}
		}()
		next, runErr := c.runSocket(ctx, conn, user.ID, inherited, out)
		close(closed)
		_ = conn.Close()
		if errors.Is(runErr, context.Canceled) {
			return nil
		}
		if runErr != nil {
			var ae *chat.AdapterError
			if errors.As(runErr, &ae) && ae.Kind != chat.Recoverable {
				return runErr
			}
			if !wait(ctx, backoff) {
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			u = c.WebSocketURL
			inherited = false
			continue
		}
		if next == "" {
			return nil
		}
		u = next
		inherited = true
		backoff = time.Second
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
