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

const (
	ReadChatScope             = "user:read:chat"
	WriteChatScope            = "user:write:chat"
	ManageBroadcastScope      = "channel:manage:broadcast"
	ManageBannedUsersScope    = "moderator:manage:banned_users"
	ManageChatMessagesScope   = "moderator:manage:chat_messages"
	ReadFollowersScope        = "moderator:read:followers"
	ReadSubscriptionsScope    = "channel:read:subscriptions"
	EventType                 = "channel.chat.message"
	FollowEventType           = "channel.follow"
	SubscribeEventType        = "channel.subscribe"
	GiftSubscriptionEventType = "channel.subscription.gift"
)

var RequiredChatScopes = []string{ReadChatScope, WriteChatScope}
var RequiredAlertScopes = []string{ReadFollowersScope, ReadSubscriptionsScope}
var RequiredRuntimeScopes = []string{ReadChatScope, WriteChatScope, ReadFollowersScope, ReadSubscriptionsScope}
var SetupScopes = []string{ReadChatScope, WriteChatScope, ManageBroadcastScope, ManageBannedUsersScope, ManageChatMessagesScope, ReadFollowersScope, ReadSubscriptionsScope}

var (
	ErrWriteScope         = errors.New("Twitch authorization lacks user:write:chat; run: streamchat setup twitch")
	ErrManageScope        = errors.New("Twitch authorization lacks channel:manage:broadcast; run: streamchat setup twitch")
	ErrBannedUsersScope   = errors.New("Twitch authorization lacks moderator:manage:banned_users; run: streamchat setup twitch")
	ErrChatMessagesScope  = errors.New("Twitch authorization lacks moderator:manage:chat_messages; run: streamchat setup twitch")
	ErrFollowersScope     = errors.New("Twitch authorization lacks moderator:read:followers; run: streamchat setup twitch")
	ErrSubscriptionsScope = errors.New("Twitch authorization lacks channel:read:subscriptions; run: streamchat setup twitch")
	ErrChatAuthentication = errors.New("Twitch chat authorization failed; run: streamchat setup twitch")
	ErrChatRateLimit      = errors.New("Twitch chat rate limit exceeded; try again shortly")
	ErrChatRejected       = errors.New("Twitch rejected the chat message")
)

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
	r, err := a.httpClient().Do(req)
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
	r, err := a.httpClient().Do(req)
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

func RequireScopes(scopes []string, required ...string) error {
	for _, scope := range required {
		if contains(scopes, scope) {
			continue
		}
		if scope == WriteChatScope {
			return ErrWriteScope
		}
		if scope == ManageBroadcastScope {
			return ErrManageScope
		}
		if scope == ManageBannedUsersScope {
			return ErrBannedUsersScope
		}
		if scope == ManageChatMessagesScope {
			return ErrChatMessagesScope
		}
		if scope == ReadFollowersScope {
			return ErrFollowersScope
		}
		if scope == ReadSubscriptionsScope {
			return ErrSubscriptionsScope
		}
		return fmt.Errorf("Twitch authorization lacks %s; run: streamchat setup twitch", scope)
	}
	return nil
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
	r, err := a.httpClient().Do(req)
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

type eventSubscription struct {
	Type      string
	Version   string
	Condition map[string]string
}

func twitchSubscriptions(broadcaster, user string) []eventSubscription {
	return []eventSubscription{
		{Type: EventType, Version: "1", Condition: map[string]string{"broadcaster_user_id": broadcaster, "user_id": user}},
		{Type: FollowEventType, Version: "2", Condition: map[string]string{"broadcaster_user_id": broadcaster, "moderator_user_id": user}},
		{Type: SubscribeEventType, Version: "1", Condition: map[string]string{"broadcaster_user_id": broadcaster}},
		{Type: GiftSubscriptionEventType, Version: "1", Condition: map[string]string{"broadcaster_user_id": broadcaster}},
	}
}

func (a *API) Subscribe(ctx context.Context, session, broadcaster, user string) error {
	refreshed := false
	for _, subscription := range twitchSubscriptions(broadcaster, user) {
		body, _ := json.Marshal(map[string]any{"type": subscription.Type, "version": subscription.Version, "condition": subscription.Condition, "transport": map[string]string{"method": "websocket", "session_id": session}})
		status, err := a.subscribe(ctx, body)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && !refreshed && a.RefreshToken != "" {
			tok, refreshErr := a.Refresh(ctx, a.RefreshToken)
			if refreshErr != nil {
				return refreshErr
			}
			a.AccessToken = tok.AccessToken
			if tok.RefreshToken != "" {
				a.RefreshToken = tok.RefreshToken
			}
			if a.OnToken != nil {
				if refreshErr = a.OnToken(tok); refreshErr != nil {
					return refreshErr
				}
			}
			refreshed = true
			status, err = a.subscribe(ctx, body)
			if err != nil {
				return err
			}
		}
		if status/100 != 2 {
			kind := chat.Terminal
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				kind = chat.Authentication
			}
			return &chat.AdapterError{Kind: kind, Op: "Twitch " + subscription.Type + " subscription", Err: fmt.Errorf("HTTP %d; verify authorization/channel and run: streamchat setup twitch", status)}
		}
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
	r, err := a.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, nil
}

func (a *API) httpClient() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return http.DefaultClient
}

type ChatSender struct {
	Auth                    *UserClient
	BroadcasterID, SenderID string
}

func NewChatSender(api *API, broadcasterID, senderID string, scopes []string) *ChatSender {
	return NewChatSenderWithUserClient(NewUserClient(api, scopes), broadcasterID, senderID)
}

func NewChatSenderWithUserClient(auth *UserClient, broadcasterID, senderID string) *ChatSender {
	return &ChatSender{Auth: auth, BroadcasterID: broadcasterID, SenderID: senderID}
}

func (s *ChatSender) Send(ctx context.Context, message string) error {
	_, err := s.SendMessage(ctx, message)
	return err
}

func (s *ChatSender) SendMessage(ctx context.Context, message string) (string, error) {
	if s.Auth == nil {
		return "", ErrChatAuthentication
	}
	if err := s.Auth.RequireScopes(WriteChatScope); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.BroadcasterID) == "" || strings.TrimSpace(s.SenderID) == "" {
		return "", errors.New("Twitch chat target is unavailable; run: streamchat setup twitch")
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("Twitch chat message is empty")
	}
	response, err := s.Auth.Do(ctx, []string{WriteChatScope}, func(accessToken string) (*http.Request, error) {
		return buildChatRequest(s.Auth.API, accessToken, s.BroadcasterID, s.SenderID, message)
	})
	if err != nil {
		return "", fmt.Errorf("Twitch chat request failed: %w", err)
	}
	defer response.Body.Close()
	var result chatSendResponse
	if response.StatusCode/100 == 2 {
		if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
			return "", errors.New("Twitch chat returned an invalid response")
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	}
	return chatSendResult(response.StatusCode, result)
}

type chatSendResponse struct {
	Data []struct {
		MessageID  string `json:"message_id"`
		IsSent     bool   `json:"is_sent"`
		DropReason *struct {
			Code string `json:"code"`
		} `json:"drop_reason"`
	} `json:"data"`
}

func buildChatRequest(api *API, accessToken, broadcasterID, senderID, message string) (*http.Request, error) {
	body, err := json.Marshal(map[string]string{
		"broadcaster_id": broadcasterID,
		"sender_id":      senderID,
		"message":        message,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(api.APIBaseURL, "/")+"/chat/messages", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Client-Id", api.ClientID)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func chatSendResult(status int, response chatSendResponse) (string, error) {
	switch status {
	case http.StatusOK:
		if len(response.Data) == 1 && response.Data[0].IsSent && response.Data[0].MessageID != "" {
			return response.Data[0].MessageID, nil
		}
		if len(response.Data) == 1 && response.Data[0].DropReason != nil && response.Data[0].DropReason.Code != "" {
			return "", fmt.Errorf("%w (%s)", ErrChatRejected, response.Data[0].DropReason.Code)
		}
		return "", ErrChatRejected
	case http.StatusUnauthorized:
		return "", ErrChatAuthentication
	case http.StatusForbidden:
		return "", fmt.Errorf("%w: sender is not permitted in this channel", ErrChatRejected)
	case http.StatusTooManyRequests:
		return "", ErrChatRateLimit
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "", fmt.Errorf("%w (HTTP %d)", ErrChatRejected, status)
	default:
		return "", fmt.Errorf("Twitch chat API failed (HTTP %d)", status)
	}
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
	thirdParty                    *thirdPartyEmotes
	mu                            sync.Mutex
	seen                          map[string]struct{}
	order                         []string
}

func New(api *API, wsURL, channel, userID string) *Client {
	return &Client{API: api, WebSocketURL: wsURL, Channel: channel, UserID: userID, Dial: defaultDial, thirdParty: newThirdPartyEmotes(nil, "", "", ""), seen: map[string]struct{}{}}
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
	UserID, UserName, UserLogin                      string
	MessageID                                        string `json:"message_id"`
	Color                                            string `json:"color"`
	Tier                                             string `json:"tier"`
	IsGift                                           bool   `json:"is_gift"`
	Total                                            int    `json:"total"`
	IsAnonymous                                      bool   `json:"is_anonymous"`
	FollowedAt                                       string `json:"followed_at"`
	Cheer                                            *struct {
		Bits int `json:"bits"`
	} `json:"cheer"`
	Badges []struct {
		SetID string `json:"set_id"`
		ID    string `json:"id"`
		Info  string `json:"info"`
	} `json:"badges"`
	Message struct {
		Text      string `json:"text"`
		Fragments []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Emote *struct {
				ID         string   `json:"id"`
				EmoteSetID string   `json:"emote_set_id"`
				OwnerID    string   `json:"owner_id"`
				Format     []string `json:"format"`
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
		UserID           string `json:"user_id"`
		UserName         string `json:"user_name"`
		UserLogin        string `json:"user_login"`
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
	e.UserID = v.UserID
	e.UserName = v.UserName
	e.UserLogin = v.UserLogin
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
	ts, err := time.Parse(time.RFC3339Nano, v.Metadata.MessageTimestamp)
	if err != nil {
		return v, nil, err
	}
	e := v.Payload.Event
	if v.Metadata.SubscriptionType == FollowEventType {
		m := chat.Message{ID: v.Metadata.MessageID, Platform: chat.PlatformTwitch, ChannelID: e.BroadcasterID, ChannelDisplayName: e.BroadcasterName, Timestamp: ts, AuthorID: e.UserID, AuthorDisplayName: e.UserName, Roles: chat.NewRoleSet(chat.RoleFollower), Text: "Followed the channel.", EventType: chat.EventOther, SafePlatformMetadata: map[string]string{"twitch_login": e.UserLogin, "twitch_broadcaster_login": e.BroadcasterLogin, "twitch_event": FollowEventType}}
		if err = m.Validate(); err != nil {
			return v, nil, err
		}
		return v, &m, nil
	}
	if v.Metadata.SubscriptionType == SubscribeEventType {
		level := twitchTierName(e.Tier)
		text := "Subscribed"
		if e.IsGift {
			text = "Received a gift subscription"
		}
		if level != "" {
			text += " at " + level
		}
		m := chat.Message{ID: v.Metadata.MessageID, Platform: chat.PlatformTwitch, ChannelID: e.BroadcasterID, ChannelDisplayName: e.BroadcasterName, Timestamp: ts, AuthorID: e.UserID, AuthorDisplayName: e.UserName, Roles: chat.NewRoleSet(chat.RoleSubscriber), Text: text + ".", EventType: chat.EventMembership, Membership: &chat.Membership{Level: level, IsGift: e.IsGift}, SafePlatformMetadata: map[string]string{"twitch_login": e.UserLogin, "twitch_broadcaster_login": e.BroadcasterLogin, "twitch_event": SubscribeEventType}}
		if err = m.Validate(); err != nil {
			return v, nil, err
		}
		return v, &m, nil
	}
	if v.Metadata.SubscriptionType == GiftSubscriptionEventType {
		actorID, actorName, actorLogin := e.UserID, e.UserName, e.UserLogin
		if e.IsAnonymous {
			actorID, actorName, actorLogin = "", "anonymous viewer", ""
		}
		m := chat.Message{ID: v.Metadata.MessageID, Platform: chat.PlatformTwitch, ChannelID: e.BroadcasterID, ChannelDisplayName: e.BroadcasterName, Timestamp: ts, AuthorID: actorID, AuthorDisplayName: actorName, Text: fmt.Sprintf("Gifted %d subscriptions.", e.Total), EventType: chat.EventMembership, Membership: &chat.Membership{Level: twitchTierName(e.Tier), GiftCount: e.Total, IsGift: true}, SafePlatformMetadata: map[string]string{"twitch_login": actorLogin, "twitch_broadcaster_login": e.BroadcasterLogin, "twitch_event": GiftSubscriptionEventType}}
		if err = m.Validate(); err != nil {
			return v, nil, err
		}
		return v, &m, nil
	}
	if v.Metadata.SubscriptionType != EventType {
		return v, nil, nil
	}
	m := chat.Message{ID: e.MessageID, Platform: chat.PlatformTwitch, ChannelID: e.BroadcasterID, ChannelDisplayName: e.BroadcasterName, Timestamp: ts, AuthorID: e.ChatterID, AuthorDisplayName: e.ChatterName, AuthorColor: e.Color, Text: e.Message.Text, EventType: chat.EventMessage, SafePlatformMetadata: map[string]string{"twitch_login": e.ChatterLogin, "twitch_broadcaster_login": e.BroadcasterLogin}}
	if e.Cheer != nil && e.Cheer.Bits > 0 {
		m.EventType = chat.EventPaid
		m.Paid = &chat.Paid{Display: fmt.Sprintf("%d Bits", e.Cheer.Bits)}
	}
	pos := 0
	for _, f := range e.Message.Fragments {
		if f.Type == "emote" && f.Emote != nil {
			length := len([]rune(f.Text))
			if length > 0 {
				m.Emotes = append(m.Emotes, chat.Emote{ID: f.Emote.ID, Name: f.Text, URL: TwitchEmoteURL(f.Emote.ID, f.Emote.Format), Start: pos, End: pos + length - 1})
			}
		}
		pos += len([]rune(f.Text))
	}
	for _, b := range e.Badges {
		m.Badges = append(m.Badges, chat.Badge{Type: b.SetID, Text: b.ID})
		switch strings.ToLower(strings.TrimSpace(b.SetID)) {
		case "broadcaster":
			m.Roles.Add(chat.RoleBroadcaster)
		case "moderator":
			m.Roles.Add(chat.RoleModerator)
		case "partner":
			m.Roles.Add(chat.RolePartner)
		case "vip":
			m.Roles.Add(chat.RoleVIP)
		case "subscriber":
			m.Roles.Add(chat.RoleSubscriber)
		}
	}
	if err = m.Validate(); err != nil {
		return v, nil, err
	}
	return v, &m, nil
}

func twitchTierName(tier string) string {
	switch strings.TrimSpace(tier) {
	case "1000":
		return "Tier 1"
	case "2000":
		return "Tier 2"
	case "3000":
		return "Tier 3"
	default:
		return strings.TrimSpace(tier)
	}
}

const twitchEmoteCDNBase = "https://static-cdn.jtvnw.net/emoticons/v2"

// TwitchEmoteURL applies Twitch's documented Emote API CDN template to
// EventSub fragment metadata. The terminal backend renders a stable first
// frame, so animated is preferred only when Twitch explicitly advertises it.
func TwitchEmoteURL(id string, formats []string) string {
	id = strings.TrimSpace(id)
	if len(id) == 0 || len(id) > 128 {
		return ""
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return ""
		}
	}
	format := ""
	for _, available := range formats {
		if strings.EqualFold(strings.TrimSpace(available), "animated") {
			format = "animated"
			break
		}
		if strings.EqualFold(strings.TrimSpace(available), "static") {
			format = "static"
		}
	}
	if format == "" {
		return ""
	}
	return twitchEmoteCDNBase + "/" + url.PathEscape(id) + "/" + format + "/dark/3.0"
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
	return c.runSocketConnected(ctx, conn, broadcaster, inherited, out, nil)
}

func (c *Client) runSocketConnected(ctx context.Context, conn wsConn, broadcaster string, inherited bool, out chan<- chat.Message, onWelcome func()) (string, error) {
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
			if onWelcome != nil {
				onWelcome()
				onWelcome = nil
			}
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
				if c.thirdParty != nil {
					*m = c.thirdParty.Enrich(*m)
				}
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
	if c.thirdParty != nil {
		// Third-party emotes are optional presentation metadata. A provider
		// outage or slow response must never delay Twitch chat from connecting.
		go func() { _ = c.thirdParty.Load(ctx, user.ID) }()
	}
	u := c.WebSocketURL
	inherited := false
	backoff := time.Second
	var previous wsConn
	var previousDone chan error
	defer func() {
		if previous != nil {
			_ = previous.Close()
		}
	}()
	for {
		conn, err := c.Dial(ctx, u)
		if err != nil {
			if !wait(ctx, backoff) {
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			if !inherited {
				u = c.WebSocketURL
			}
			continue
		}
		if inherited && previous != nil && previousDone == nil {
			previousDone = make(chan error, 1)
			old := previous
			done := previousDone
			go func() {
				_, oldErr := c.runSocket(ctx, old, user.ID, true, out)
				done <- oldErr
			}()
		}
		closed := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-closed:
			}
		}()
		adopted := !inherited
		next, runErr := c.runSocketConnected(ctx, conn, user.ID, inherited, out, func() {
			adopted = true
			if previous != nil {
				_ = previous.Close()
				previous = nil
				previousDone = nil
			}
		})
		close(closed)
		if next == "" {
			_ = conn.Close()
		}
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
			if adopted {
				u = c.WebSocketURL
				inherited = false
			}
			continue
		}
		if next == "" {
			return nil
		}
		if previous != nil {
			_ = previous.Close()
		}
		previous = conn
		previousDone = nil
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
