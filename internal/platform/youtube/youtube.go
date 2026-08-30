package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
)

var ErrChatEnded = errors.New("YouTube live chat ended")
var ErrNoActiveBroadcast = errors.New("YouTube account has no active broadcast")

const (
	ForceSSLScope = "https://www.googleapis.com/auth/youtube.force-ssl"
	AuthorizeURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL      = "https://oauth2.googleapis.com/token"
)

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

type OAuthClient struct {
	HTTP                   *http.Client
	TokenURL               string
	ClientID, ClientSecret string
}

func (c OAuthClient) token(ctx context.Context, form url.Values) (Token, error) {
	endpoint := c.TokenURL
	if endpoint == "" {
		endpoint = TokenURL
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, &chat.AdapterError{Kind: chat.Recoverable, Op: "YouTube OAuth", Err: errors.New("request failed")}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Token{}, &chat.AdapterError{Kind: chat.Authentication, Op: "YouTube OAuth", Err: fmt.Errorf("credential exchange rejected (HTTP %d)", resp.StatusCode)}
	}
	var tok Token
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return Token{}, errors.New("YouTube OAuth returned an invalid response")
	}
	if tok.AccessToken == "" {
		return Token{}, errors.New("YouTube OAuth response did not contain an access token")
	}
	return tok, nil
}

func (c OAuthClient) Exchange(ctx context.Context, code, redirectURI, verifier string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
}

func (c OAuthClient) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "refresh_token": {refreshToken}})
}

type Client struct {
	HTTP                                  *http.Client
	BaseURL, APIKey, AccessToken, VideoID string
	ClientID, ClientSecret, RefreshToken  string
	TokenURL                              string
	TokenExpiry                           time.Time
	OnToken                               func(Token) error
	Sleep                                 func(context.Context, time.Duration) error
	Rand                                  *rand.Rand
	RetryDelay                            time.Duration
}

func New(httpc *http.Client, base, key, token, video string) *Client {
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{HTTP: httpc, BaseURL: strings.TrimRight(base, "/"), APIKey: key, AccessToken: token, VideoID: video, TokenURL: TokenURL, Sleep: sleep, Rand: rand.New(rand.NewSource(time.Now().UnixNano())), RetryDelay: 15 * time.Second}
}

// ParseVideoID accepts the identifiers and URL forms users commonly copy from
// YouTube without treating arbitrary URL path text as an ID.
func ParseVideoID(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", errors.New("enter a YouTube live URL or video ID")
	}
	if !strings.Contains(s, "://") {
		if strings.ContainsAny(s, "/?#& ") {
			return "", errors.New("invalid YouTube video ID")
		}
		return s, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("invalid YouTube URL")
	}
	h := strings.ToLower(u.Hostname())
	var id string
	switch h {
	case "youtu.be":
		id = strings.Split(strings.Trim(u.Path, "/"), "/")[0]
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else {
			p := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(p) >= 2 && (p[0] == "live" || p[0] == "shorts" || p[0] == "embed") {
				id = p[1]
			}
		}
	default:
		return "", errors.New("YouTube URL must use youtube.com or youtu.be")
	}
	if id == "" || strings.ContainsAny(id, "/?#& ") {
		return "", errors.New("YouTube URL does not contain a video ID")
	}
	return id, nil
}
func (c *Client) Name() string { return "youtube" }
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (c *Client) refresh(ctx context.Context, force bool) error {
	if c.RefreshToken == "" || c.ClientID == "" || c.ClientSecret == "" {
		return nil
	}
	if !force && c.AccessToken != "" && (c.TokenExpiry.IsZero() || time.Until(c.TokenExpiry) > time.Minute) {
		return nil
	}
	oc := OAuthClient{HTTP: c.HTTP, TokenURL: c.TokenURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}
	tok, err := oc.Refresh(ctx, c.RefreshToken)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = c.RefreshToken
	}
	c.AccessToken = tok.AccessToken
	c.RefreshToken = tok.RefreshToken
	c.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if c.OnToken != nil {
		return c.OnToken(tok)
	}
	return nil
}

func (c *Client) request(ctx context.Context, path string, q url.Values, out any) error {
	return c.requestJSON(ctx, http.MethodGet, path, q, nil, out)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, q url.Values, body, out any) error {
	if err := c.refresh(ctx, false); err != nil {
		return err
	}
	return c.requestAttempt(ctx, method, path, q, body, out, true)
}

func (c *Client) requestAttempt(ctx context.Context, method, path string, q url.Values, body, out any, retryAuth bool) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	endpoint := c.BaseURL + path
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if e != nil {
		return e
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	if c.APIKey != "" {
		req.Header.Set("X-Goog-Api-Key", c.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, e := c.HTTP.Do(req)
	if e != nil {
		return &chat.AdapterError{Kind: chat.Recoverable, Op: "YouTube request", Err: e}
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		reason := errorReason(b)
		if r.StatusCode == http.StatusUnauthorized && retryAuth && c.RefreshToken != "" {
			if err := c.refresh(ctx, true); err != nil {
				return err
			}
			return c.requestAttempt(ctx, method, path, q, body, out, false)
		}
		if reason == "liveChatEnded" {
			return ErrChatEnded
		}
		kind := chat.Terminal
		if r.StatusCode == 429 || r.StatusCode >= 500 || reason == "rateLimitExceeded" {
			kind = chat.Recoverable
		}
		if r.StatusCode == 401 || r.StatusCode == 403 && (reason == "forbidden" || reason == "insufficientPermissions") {
			kind = chat.Authentication
		}
		if kind == chat.Authentication {
			return &chat.AdapterError{Kind: kind, Op: "YouTube API", Err: fmt.Errorf("credential rejected (HTTP %d, %s); run: streamchat setup youtube-server", r.StatusCode, reason)}
		}
		return &chat.AdapterError{Kind: kind, Op: "YouTube API", Err: fmt.Errorf("HTTP %d (%s)", r.StatusCode, reason)}
	}
	if out == nil || r.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(out)
}

func (c *Client) ValidateCredential(ctx context.Context) error {
	var v struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	// A single videos.list request costs one quota unit and avoids search.list.
	return c.request(ctx, "/videos", url.Values{"part": {"id"}, "id": {"jNQXAC9IVRw"}}, &v)
}
func errorReason(b []byte) string {
	var v struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &v)
	if len(v.Error.Errors) > 0 {
		return v.Error.Errors[0].Reason
	}
	return "unknown"
}
func (c *Client) Discover(ctx context.Context) (string, string, error) {
	if c.VideoID == "" {
		return c.discoverActive(ctx)
	}
	var v struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				LiveChatID string `json:"activeLiveChatId"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	e := c.request(ctx, "/videos", url.Values{"part": {"snippet,liveStreamingDetails"}, "id": {c.VideoID}}, &v)
	if e != nil {
		return "", "", e
	}
	if len(v.Items) == 0 {
		return "", "", errors.New("YouTube video or broadcast not found")
	}
	if v.Items[0].Snippet.LiveChatID == "" {
		return "", "", errors.New("YouTube video has no active live chat")
	}
	return v.Items[0].Snippet.LiveChatID, v.Items[0].ID, e
}

func (c *Client) discoverActive(ctx context.Context) (string, string, error) {
	var v struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				LiveChatID string `json:"liveChatId"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := c.request(ctx, "/liveBroadcasts", url.Values{"part": {"id,snippet"}, "broadcastStatus": {"active"}, "broadcastType": {"all"}, "maxResults": {"5"}}, &v); err != nil {
		return "", "", err
	}
	for _, item := range v.Items {
		if item.Snippet.LiveChatID != "" {
			return item.Snippet.LiveChatID, item.ID, nil
		}
	}
	return "", "", ErrNoActiveBroadcast
}

type StreamStatus struct {
	VideoID, LiveChatID, Title, Category, CategoryID string
	ViewerCount                                      int
	Live                                             bool
}

type sendResponse struct {
	ID string `json:"id"`
}

func (c *Client) SendMessage(ctx context.Context, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("message must not be empty")
	}
	chatID, _, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]any{"snippet": map[string]any{"liveChatId": chatID, "type": "textMessageEvent", "textMessageDetails": map[string]string{"messageText": message}}}
	var result sendResponse
	err = c.requestJSON(ctx, http.MethodPost, "/liveChat/messages", url.Values{"part": {"snippet"}}, body, &result)
	return result.ID, err
}

func (c *Client) DeleteMessage(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("YouTube message ID is required")
	}
	return c.requestJSON(ctx, http.MethodDelete, "/liveChat/messages", url.Values{"id": {messageID}}, nil, nil)
}

func (c *Client) Ban(ctx context.Context, channelID string, durationSeconds int) (string, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", errors.New("YouTube channel ID is required")
	}
	chatID, _, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	typeName := "permanent"
	snippet := map[string]any{"liveChatId": chatID, "type": typeName, "bannedUserDetails": map[string]string{"channelId": channelID}}
	if durationSeconds > 0 {
		typeName = "temporary"
		snippet["type"] = typeName
		snippet["banDurationSeconds"] = durationSeconds
	}
	var result struct {
		ID string `json:"id"`
	}
	err = c.requestJSON(ctx, http.MethodPost, "/liveChat/bans", url.Values{"part": {"snippet"}}, map[string]any{"snippet": snippet}, &result)
	return result.ID, err
}

func (c *Client) Status(ctx context.Context) (StreamStatus, error) {
	chatID, videoID, err := c.Discover(ctx)
	if err != nil {
		if errors.Is(err, ErrNoActiveBroadcast) {
			return StreamStatus{}, nil
		}
		return StreamStatus{}, err
	}
	var videos struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title, CategoryID string
			} `json:"snippet"`
			Live struct {
				ConcurrentViewers string `json:"concurrentViewers"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	if err = c.request(ctx, "/videos", url.Values{"part": {"snippet,liveStreamingDetails"}, "id": {videoID}}, &videos); err != nil {
		return StreamStatus{}, err
	}
	if len(videos.Items) == 0 {
		return StreamStatus{}, errors.New("YouTube active broadcast was not found")
	}
	item := videos.Items[0]
	viewers, _ := strconv.Atoi(item.Live.ConcurrentViewers)
	category := item.Snippet.CategoryID
	if item.Snippet.CategoryID != "" {
		var categories struct {
			Items []struct {
				Snippet struct {
					Title string `json:"title"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if e := c.request(ctx, "/videoCategories", url.Values{"part": {"snippet"}, "id": {item.Snippet.CategoryID}}, &categories); e == nil && len(categories.Items) > 0 {
			category = categories.Items[0].Snippet.Title
		}
	}
	return StreamStatus{VideoID: videoID, LiveChatID: chatID, Title: item.Snippet.Title, Category: category, CategoryID: item.Snippet.CategoryID, ViewerCount: viewers, Live: true}, nil
}

func (c *Client) UpdateTitle(ctx context.Context, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title must not be empty")
	}
	return c.updateSnippet(ctx, func(snippet map[string]any) { snippet["title"] = title })
}

func (c *Client) UpdateCategory(ctx context.Context, query string) (string, error) {
	categoryID, name, err := c.ResolveCategory(ctx, query)
	if err != nil {
		return "", err
	}
	err = c.updateSnippet(ctx, func(snippet map[string]any) { snippet["categoryId"] = categoryID })
	return name, err
}

func (c *Client) ResolveCategory(ctx context.Context, query string) (string, string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", "", errors.New("category must not be empty")
	}
	var categories struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := c.request(ctx, "/videoCategories", url.Values{"part": {"snippet"}, "regionCode": {"US"}}, &categories); err != nil {
		return "", "", err
	}
	var partial []struct{ id, name string }
	for _, item := range categories.Items {
		if item.ID == query || strings.EqualFold(item.Snippet.Title, query) {
			return item.ID, item.Snippet.Title, nil
		}
		if strings.Contains(strings.ToLower(item.Snippet.Title), strings.ToLower(query)) {
			partial = append(partial, struct{ id, name string }{item.ID, item.Snippet.Title})
		}
	}
	if len(partial) == 1 {
		return partial[0].id, partial[0].name, nil
	}
	if len(partial) > 1 {
		return "", "", errors.New("YouTube category is ambiguous; enter the exact category name or ID")
	}
	return "", "", errors.New("YouTube category was not found")
}

func (c *Client) updateSnippet(ctx context.Context, change func(map[string]any)) error {
	_, videoID, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	var videos struct {
		Items []struct {
			ID      string         `json:"id"`
			Snippet map[string]any `json:"snippet"`
		} `json:"items"`
	}
	if err = c.request(ctx, "/videos", url.Values{"part": {"snippet"}, "id": {videoID}}, &videos); err != nil {
		return err
	}
	if len(videos.Items) == 0 {
		return errors.New("YouTube active broadcast was not found")
	}
	snippet := videos.Items[0].Snippet
	change(snippet)
	return c.requestJSON(ctx, http.MethodPut, "/videos", url.Values{"part": {"snippet"}}, map[string]any{"id": videoID, "snippet": snippet}, &struct{}{})
}

type listResponse struct {
	NextPageToken   string       `json:"nextPageToken"`
	PollingInterval int          `json:"pollingIntervalMillis"`
	OfflineAt       string       `json:"offlineAt"`
	Items           []apiMessage `json:"items"`
}
type apiMessage struct {
	ID      string `json:"id"`
	Snippet struct {
		Type, LiveChatID, AuthorChannelID, PublishedAt, DisplayMessage string
		SuperChat                                                      *struct {
			AmountMicros                               string `json:"amountMicros"`
			Currency, AmountDisplayString, UserComment string
		} `json:"superChatDetails"`
		Member *struct {
			MemberLevelName string `json:"memberLevelName"`
			UserComment     string `json:"userComment"`
		} `json:"memberMilestoneChatDetails"`
	} `json:"snippet"`
	Author struct {
		ChannelID, DisplayName                      string
		IsChatOwner, IsChatSponsor, IsChatModerator bool
	} `json:"authorDetails"`
}

func ParseMessage(v apiMessage, channelID, channelName string) (chat.Message, error) {
	ts, e := time.Parse(time.RFC3339Nano, v.Snippet.PublishedAt)
	if e != nil {
		return chat.Message{}, e
	}
	m := chat.Message{ID: v.ID, Platform: chat.PlatformYouTube, ChannelID: channelID, ChannelDisplayName: channelName, Timestamp: ts, AuthorID: v.Author.ChannelID, AuthorDisplayName: v.Author.DisplayName, Text: v.Snippet.DisplayMessage, EventType: chat.EventOther, SafePlatformMetadata: map[string]string{"youtube_type": v.Snippet.Type}}
	switch v.Snippet.Type {
	case "textMessageEvent":
		m.EventType = chat.EventMessage
	case "superChatEvent", "superStickerEvent":
		m.EventType = chat.EventPaid
		if v.Snippet.SuperChat != nil {
			n, _ := strconv.ParseInt(v.Snippet.SuperChat.AmountMicros, 10, 64)
			m.Paid = &chat.Paid{AmountMicros: n, Currency: v.Snippet.SuperChat.Currency, Display: v.Snippet.SuperChat.AmountDisplayString}
			if v.Snippet.SuperChat.UserComment != "" {
				m.Text = v.Snippet.SuperChat.UserComment
			}
		}
	case "newSponsorEvent", "memberMilestoneChatEvent", "membershipGiftingEvent", "giftMembershipReceivedEvent":
		m.EventType = chat.EventMembership
		m.Membership = &chat.Membership{}
		if v.Snippet.Member != nil {
			m.Membership.Level = v.Snippet.Member.MemberLevelName
			if v.Snippet.Member.UserComment != "" {
				m.Text = v.Snippet.Member.UserComment
			}
		}
	case "userBannedEvent", "messageDeletedEvent", "tombstone":
		m.EventType = chat.EventModeration
	case "chatEndedEvent":
		m.EventType = chat.EventSystem
	default:
		m.EventType = chat.EventOther
	}
	if v.Author.IsChatOwner {
		m.Roles.Add(chat.RoleBroadcaster)
		m.Badges = append(m.Badges, chat.Badge{Type: "owner"})
	}
	if v.Author.IsChatModerator {
		m.Roles.Add(chat.RoleModerator)
		m.Badges = append(m.Badges, chat.Badge{Type: "moderator"})
	}
	if v.Author.IsChatSponsor {
		m.Roles.Add(chat.RoleSubscriber)
		m.Badges = append(m.Badges, chat.Badge{Type: "member"})
	}
	return m, nil
}
func (c *Client) run(ctx context.Context, out chan<- chat.Message, streaming bool) error {
	chatID, channelID, e := c.Discover(ctx)
	if e != nil {
		return e
	}
	token := ""
	backoff := time.Second
	for {
		var v listResponse
		q := url.Values{"liveChatId": {chatID}, "part": {"id,snippet,authorDetails"}, "maxResults": {"2000"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		path := "/liveChat/messages"
		if streaming {
			path = "/liveChat/messages/stream"
			q.Del("maxResults")
		}
		e = c.request(ctx, path, q, &v)
		if e != nil {
			if errors.Is(e, ErrChatEnded) {
				return nil
			}
			var ae *chat.AdapterError
			if errors.As(e, &ae) && ae.Kind == chat.Recoverable {
				j := time.Duration(c.Rand.Int63n(int64(backoff/2 + 1)))
				if c.Sleep(ctx, backoff+j) != nil {
					return ctx.Err()
				}
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				continue
			}
			return e
		}
		backoff = time.Second
		if v.NextPageToken != "" {
			token = v.NextPageToken
		}
		for _, x := range v.Items {
			m, pe := ParseMessage(x, channelID, "")
			if pe != nil {
				continue
			}
			select {
			case out <- m:
			case <-ctx.Done():
				return ctx.Err()
			}
			if x.Snippet.Type == "chatEndedEvent" {
				return nil
			}
		}
		if v.OfflineAt != "" {
			return nil
		}
		if streaming {
			continue
		}
		d := time.Duration(v.PollingInterval) * time.Millisecond
		if d <= 0 {
			d = time.Second
		}
		if c.Sleep(ctx, d) != nil {
			return ctx.Err()
		}
	}
}

func (c *Client) Run(ctx context.Context, out chan<- chat.Message) error {
	return c.run(ctx, out, false)
}

// RunServer continuously discovers the authenticated account's active
// broadcast and reconnects the official streamList transport using its page
// token. This keeps an unattended utility VM ready between broadcasts.
func (c *Client) RunServer(ctx context.Context, out chan<- chat.Message) error {
	for {
		err := c.run(ctx, out, true)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, ErrNoActiveBroadcast) {
			var ae *chat.AdapterError
			if !errors.As(err, &ae) || ae.Kind != chat.Recoverable {
				return err
			}
		}
		if c.Sleep(ctx, c.RetryDelay) != nil {
			return nil
		}
	}
}
