package kick

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const ChatMessageManageScope = "moderation:chat_message:manage"

var (
	ErrChatDeleteAuthentication = errors.New("Kick chat-message deletion authentication failed; run: streamchat setup kick")
	ErrChatDeleteScope          = errors.New("Kick chat-message deletion permission is missing; enable moderation:chat_message:manage and rerun: streamchat setup kick")
	ErrChatDeleteNotFound       = errors.New("Kick chat message is already deleted or no longer available")
	ErrChatDeleteRateLimit      = errors.New("Kick chat-message deletion rate limit exceeded; try again later")
)

type ChatDeleteClient struct {
	HTTP                 *http.Client
	BaseURL, AccessToken string
}

// DeleteMessage uses Kick's official per-message moderation endpoint.
func (c ChatDeleteClient) DeleteMessage(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Kick message ID is required")
	}
	if c.AccessToken == "" {
		return ErrChatDeleteAuthentication
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(c.BaseURL, "/")+"/chat/"+url.PathEscape(messageID), nil)
	if err != nil {
		return errors.New("create Kick chat-message deletion request")
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return errors.New("Kick chat-message deletion request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrChatDeleteAuthentication
	case http.StatusForbidden:
		return ErrChatDeleteScope
	case http.StatusNotFound:
		return ErrChatDeleteNotFound
	case http.StatusTooManyRequests:
		return ErrChatDeleteRateLimit
	default:
		return fmt.Errorf("Kick chat-message deletion failed (HTTP %d)", response.StatusCode)
	}
}
