package discordnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBaseURL = "https://discord.com/api/v10"

type Client struct {
	channelID string
	token     string
	baseURL   string
	http      *http.Client
}

func New(channelID, token string) (*Client, error) {
	return newClient(channelID, token, defaultAPIBaseURL, &http.Client{Timeout: 10 * time.Second})
}

func newClient(channelID, token, baseURL string, httpClient *http.Client) (*Client, error) {
	channelID = strings.TrimSpace(channelID)
	token = strings.TrimSpace(token)
	if channelID == "" {
		return nil, errors.New("Discord channel ID is required")
	}
	if token == "" {
		return nil, errors.New("Discord bot token is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{channelID: channelID, token: token, baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}, nil
}

func (c *Client) Send(ctx context.Context, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("Discord message must not be blank")
	}
	payload, err := json.Marshal(struct {
		Content         string          `json:"content"`
		AllowedMentions allowedMentions `json:"allowed_mentions"`
	}{
		Content: content,
		AllowedMentions: allowedMentions{
			Parse: []string{"everyone"},
			Users: []string{},
			Roles: []string{},
		},
	})
	if err != nil {
		return fmt.Errorf("encode Discord message: %w", err)
	}
	endpoint := c.baseURL + "/channels/" + url.PathEscape(c.channelID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Discord message request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send Discord message: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Discord rejected the message with HTTP %d", response.StatusCode)
	}
	return nil
}

// allowedMentions deliberately permits only @everyone. User and role mentions
// embedded in titles, channel names, or future configurable text stay inert.
type allowedMentions struct {
	Parse []string `json:"parse"`
	Users []string `json:"users"`
	Roles []string `json:"roles"`
}
