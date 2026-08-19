package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	MinTimeoutSeconds = int64(1)
	MaxTimeoutSeconds = int64(1209600)
)

var (
	ErrModerationAuthentication = errors.New("Twitch moderation authentication failed; run: streamchat setup twitch")
	ErrModerationPermission     = errors.New("the authenticated Twitch user is not permitted to moderate the configured channel")
	ErrModerationRateLimit      = errors.New("Twitch moderation rate limit exceeded; try again shortly")
	ErrModerationConflict       = errors.New("Twitch moderation conflicted with another ban-state update; try again")
	ErrModerationUserNotFound   = errors.New("Twitch user was not found")
	ErrModerationTarget         = errors.New("the Twitch broadcaster or authenticated moderator cannot be targeted by this moderation command")
	ErrAlreadyBanned            = errors.New("Twitch user is already banned")
	ErrChatDeleteNotFound       = errors.New("Twitch chat message is no longer available for deletion")
	ErrChatDeleteProtected      = errors.New("Twitch does not allow individual deletion of broadcaster or other moderator messages")
	ErrChatDeleteRejected       = errors.New("Twitch rejected the chat clear request")
)

type ModerationClient struct {
	Auth                       *UserClient
	BroadcasterID, ModeratorID string
}

func NewModerationClient(auth *UserClient, broadcasterID, moderatorID string) *ModerationClient {
	return &ModerationClient{Auth: auth, BroadcasterID: broadcasterID, ModeratorID: moderatorID}
}

func ParseTimeoutDuration(value string) (int64, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 {
		return 0, twitchTimeoutDurationError()
	}
	amount, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, twitchTimeoutDurationError()
	}
	var multiplier int64
	switch value[len(value)-1] {
	case 's':
		multiplier = 1
	case 'm':
		multiplier = 60
	case 'h':
		multiplier = 60 * 60
	case 'd':
		multiplier = 24 * 60 * 60
	default:
		return 0, twitchTimeoutDurationError()
	}
	if amount > MaxTimeoutSeconds/multiplier {
		return 0, twitchTimeoutDurationError()
	}
	seconds := amount * multiplier
	if seconds < MinTimeoutSeconds || seconds > MaxTimeoutSeconds {
		return 0, twitchTimeoutDurationError()
	}
	return seconds, nil
}

func twitchTimeoutDurationError() error {
	return errors.New("Twitch timeout duration must be 1s to 14d using whole s, m, h, or d units")
}

func (c *ModerationClient) Ban(ctx context.Context, username string) (User, error) {
	return c.moderate(ctx, username, nil)
}

func (c *ModerationClient) Timeout(ctx context.Context, username string, seconds int64) (User, error) {
	if seconds < MinTimeoutSeconds || seconds > MaxTimeoutSeconds {
		return User{}, twitchTimeoutDurationError()
	}
	return c.moderate(ctx, username, &seconds)
}

func (c *ModerationClient) moderate(ctx context.Context, username string, duration *int64) (User, error) {
	if err := c.ready(ManageBannedUsersScope); err != nil {
		return User{}, err
	}
	user, err := c.ResolveUser(ctx, username)
	if err != nil {
		return User{}, err
	}
	if user.ID == c.BroadcasterID || user.ID == c.ModeratorID {
		return User{}, ErrModerationTarget
	}
	data := struct {
		UserID   string `json:"user_id"`
		Duration *int64 `json:"duration,omitempty"`
	}{UserID: user.ID, Duration: duration}
	body, err := json.Marshal(struct {
		Data any `json:"data"`
	}{Data: data})
	if err != nil {
		return User{}, err
	}
	response, err := c.Auth.Do(ctx, []string{ManageBannedUsersScope}, func(accessToken string) (*http.Request, error) {
		path := "/moderation/bans?broadcaster_id=" + url.QueryEscape(c.BroadcasterID) + "&moderator_id=" + url.QueryEscape(c.ModeratorID)
		return c.request(http.MethodPost, path, bytes.NewReader(body), accessToken)
	})
	if err != nil {
		return User{}, safeModerationRequestError(err)
	}
	defer response.Body.Close()
	message := twitchErrorMessage(response.Body)
	if response.StatusCode != http.StatusOK {
		return User{}, moderationStatusError(response.StatusCode, message)
	}
	return user, nil
}

func (c *ModerationClient) ResolveUser(ctx context.Context, username string) (User, error) {
	parsed, err := ParseChannel(username)
	if err != nil {
		return User{}, errors.New("enter a Twitch username, @username, or channel URL")
	}
	username = parsed
	response, err := c.Auth.Do(ctx, nil, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodGet, "/users?login="+url.QueryEscape(username), nil, accessToken)
	})
	if err != nil {
		return User{}, safeModerationRequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return User{}, moderationStatusError(response.StatusCode, "")
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return User{}, errors.New("Twitch user lookup returned an invalid response")
	}
	if len(payload.Data) != 1 || payload.Data[0].ID == "" {
		return User{}, fmt.Errorf("%w: %s", ErrModerationUserNotFound, username)
	}
	return User(payload.Data[0]), nil
}

func (c *ModerationClient) ClearChat(ctx context.Context) error {
	return c.deleteChat(ctx, "")
}

func (c *ModerationClient) DeleteMessage(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Twitch message ID is required")
	}
	return c.deleteChat(ctx, messageID)
}

func (c *ModerationClient) deleteChat(ctx context.Context, messageID string) error {
	if err := c.ready(ManageChatMessagesScope); err != nil {
		return err
	}
	path := "/moderation/chat?broadcaster_id=" + url.QueryEscape(c.BroadcasterID) + "&moderator_id=" + url.QueryEscape(c.ModeratorID)
	if messageID != "" {
		path += "&message_id=" + url.QueryEscape(messageID)
	}
	response, err := c.Auth.Do(ctx, []string{ManageChatMessagesScope}, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodDelete, path, nil, accessToken)
	})
	if err != nil {
		return safeModerationRequestError(err)
	}
	defer response.Body.Close()
	message := twitchErrorMessage(response.Body)
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	switch response.StatusCode {
	case http.StatusBadRequest:
		if messageID == "" {
			return ErrChatDeleteRejected
		}
		return ErrChatDeleteProtected
	case http.StatusNotFound:
		return ErrChatDeleteNotFound
	default:
		return moderationStatusError(response.StatusCode, message)
	}
}

func (c *ModerationClient) ready(scope string) error {
	if c.Auth == nil || c.Auth.API == nil || c.BroadcasterID == "" || c.ModeratorID == "" {
		return ErrModerationAuthentication
	}
	return c.Auth.RequireScopes(scope)
}

func (c *ModerationClient) request(method, path string, body io.Reader, accessToken string) (*http.Request, error) {
	request, err := http.NewRequest(method, strings.TrimRight(c.Auth.API.APIBaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Client-Id", c.Auth.API.ClientID)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func safeModerationRequestError(err error) error {
	if errors.Is(err, ErrBannedUsersScope) || errors.Is(err, ErrChatMessagesScope) {
		return err
	}
	return fmt.Errorf("Twitch moderation request failed: %w", err)
}

func twitchErrorMessage(body io.Reader) string {
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&payload)
	return strings.ToLower(payload.Message)
}

func moderationStatusError(status int, message string) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrModerationAuthentication
	case http.StatusForbidden:
		return ErrModerationPermission
	case http.StatusTooManyRequests:
		return ErrModerationRateLimit
	case http.StatusConflict:
		return ErrModerationConflict
	case http.StatusBadRequest:
		switch {
		case strings.Contains(message, "already banned"):
			return ErrAlreadyBanned
		case strings.Contains(message, "may not be banned"), strings.Contains(message, "may not be timed out"):
			return ErrModerationTarget
		default:
			return errors.New("Twitch rejected the moderation request (HTTP 400)")
		}
	case http.StatusNotFound:
		return ErrModerationUserNotFound
	default:
		return fmt.Errorf("Twitch moderation API failed (HTTP %d)", status)
	}
}
