package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SleepyMario/streamchat/internal/chat"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrChatEnded = errors.New("YouTube live chat ended")

type Client struct {
	HTTP                                  *http.Client
	BaseURL, APIKey, AccessToken, VideoID string
	Sleep                                 func(context.Context, time.Duration) error
	Rand                                  *rand.Rand
}

func New(httpc *http.Client, base, key, token, video string) *Client {
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{HTTP: httpc, BaseURL: strings.TrimRight(base, "/"), APIKey: key, AccessToken: token, VideoID: video, Sleep: sleep, Rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
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
func (c *Client) request(ctx context.Context, path string, q url.Values, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path+"?"+q.Encode(), nil)
	if e != nil {
		return e
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	if c.APIKey != "" {
		req.Header.Set("X-Goog-Api-Key", c.APIKey)
	}
	r, e := c.HTTP.Do(req)
	if e != nil {
		return &chat.AdapterError{Kind: chat.Recoverable, Op: "YouTube request", Err: e}
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		reason := errorReason(b)
		if reason == "liveChatEnded" {
			return ErrChatEnded
		}
		kind := chat.Terminal
		if r.StatusCode == 429 || r.StatusCode >= 500 || reason == "rateLimitExceeded" {
			kind = chat.Recoverable
		}
		if r.StatusCode == 401 || r.StatusCode == 403 && reason == "forbidden" {
			kind = chat.Authentication
		}
		if kind == chat.Authentication {
			return &chat.AdapterError{Kind: kind, Op: "YouTube API", Err: fmt.Errorf("credential rejected (HTTP %d, %s); run: streamchat setup youtube", r.StatusCode, reason)}
		}
		return &chat.AdapterError{Kind: kind, Op: "YouTube API", Err: fmt.Errorf("HTTP %d (%s)", r.StatusCode, reason)}
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
		m.Roles = append(m.Roles, "owner")
		m.Badges = append(m.Badges, chat.Badge{Type: "owner"})
	}
	if v.Author.IsChatModerator {
		m.Roles = append(m.Roles, "moderator")
		m.Badges = append(m.Badges, chat.Badge{Type: "moderator"})
	}
	if v.Author.IsChatSponsor {
		m.Roles = append(m.Roles, "member")
		m.Badges = append(m.Badges, chat.Badge{Type: "member"})
	}
	return m, nil
}
func (c *Client) Run(ctx context.Context, out chan<- chat.Message) error {
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
		e = c.request(ctx, "/liveChat/messages", q, &v)
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
		d := time.Duration(v.PollingInterval) * time.Millisecond
		if d <= 0 {
			d = time.Second
		}
		if c.Sleep(ctx, d) != nil {
			return ctx.Err()
		}
	}
}
