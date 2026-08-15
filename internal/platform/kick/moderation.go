package kick

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
	ChannelReadScope   = "channel:read"
	ModerationBanScope = "moderation:ban"
	MinTimeoutMinutes  = int64(1)
	MaxTimeoutMinutes  = int64(10080)
)

var (
	ErrModerationAuthentication = errors.New("Kick moderation authentication failed")
	ErrModerationScope          = errors.New("Kick moderation authorization is missing; enable channel:read and moderation:ban, then run: streamchat setup kick")
	ErrModerationPermission     = errors.New("Kick moderation permission denied; enable moderation:ban and rerun streamchat setup kick, and ensure the authenticated account can moderate the configured channel")
	ErrModerationRateLimit      = errors.New("Kick moderation API rate limit exceeded; try again later")
	ErrModerationUserNotFound   = errors.New("Kick user not found")
)

type ModerationUser struct {
	ID       int64
	Username string
}

type AmbiguousModerationUserError struct {
	Username string
}

func (e *AmbiguousModerationUserError) Error() string {
	return fmt.Sprintf("multiple Kick users exactly match %q; no moderation action was taken", e.Username)
}

type ModerationClient struct {
	HTTP                 *http.Client
	BaseURL, AccessToken string
}

func ParseTimeoutDuration(value string) (int64, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 {
		return 0, timeoutDurationError()
	}
	amount, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, timeoutDurationError()
	}
	var minutes int64
	switch value[len(value)-1] {
	case 's':
		if amount%60 != 0 {
			return 0, errors.New("Kick's official API accepts whole-minute timeouts only (1–10080 minutes); use at least 1m")
		}
		minutes = amount / 60
	case 'm':
		minutes = amount
	case 'h':
		if amount > MaxTimeoutMinutes/60 {
			return 0, timeoutDurationError()
		}
		minutes = amount * 60
	default:
		return 0, timeoutDurationError()
	}
	if minutes < MinTimeoutMinutes || minutes > MaxTimeoutMinutes {
		return 0, timeoutDurationError()
	}
	return minutes, nil
}

func timeoutDurationError() error {
	return errors.New("timeout duration must use s, m, or h and equal 1–10080 whole minutes (maximum 168h)")
}

func (c ModerationClient) Ban(ctx context.Context, broadcaster, username string) (ModerationUser, error) {
	return c.moderate(ctx, broadcaster, username, nil)
}

func (c ModerationClient) Timeout(ctx context.Context, broadcaster, username string, minutes int64) (ModerationUser, error) {
	if minutes < MinTimeoutMinutes || minutes > MaxTimeoutMinutes {
		return ModerationUser{}, timeoutDurationError()
	}
	return c.moderate(ctx, broadcaster, username, &minutes)
}

func (c ModerationClient) moderate(ctx context.Context, broadcaster, username string, duration *int64) (ModerationUser, error) {
	if c.AccessToken == "" {
		return ModerationUser{}, fmt.Errorf("%w; run: streamchat setup kick", ErrModerationAuthentication)
	}
	broadcasterID, err := strconv.ParseInt(broadcaster, 10, 64)
	if err != nil || broadcasterID <= 0 {
		return ModerationUser{}, errors.New("Kick broadcaster user ID is missing; run: streamchat setup kick")
	}
	user, err := c.ResolveUser(ctx, username)
	if err != nil {
		return ModerationUser{}, err
	}
	body, err := json.Marshal(struct {
		BroadcasterUserID int64  `json:"broadcaster_user_id"`
		UserID            int64  `json:"user_id"`
		Duration          *int64 `json:"duration,omitempty"`
	}{BroadcasterUserID: broadcasterID, UserID: user.ID, Duration: duration})
	if err != nil {
		return ModerationUser{}, err
	}
	req, err := c.request(ctx, http.MethodPost, "/moderation/bans", bytes.NewReader(body))
	if err != nil {
		return ModerationUser{}, err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return ModerationUser{}, fmt.Errorf("Kick moderation API request failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if err = moderationStatusError(response, true); err != nil {
		return ModerationUser{}, err
	}
	return user, nil
}

func (c ModerationClient) ResolveUser(ctx context.Context, username string) (ModerationUser, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return ModerationUser{}, errors.New("Kick username is required")
	}
	req, err := c.request(ctx, http.MethodGet, "/channels?slug="+url.QueryEscape(username), nil)
	if err != nil {
		return ModerationUser{}, err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return ModerationUser{}, fmt.Errorf("Kick user lookup failed: %w", err)
	}
	defer response.Body.Close()
	if err = moderationStatusError(response, false); err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return ModerationUser{}, err
	}
	var payload struct {
		Data []struct {
			BroadcasterUserID int64  `json:"broadcaster_user_id"`
			Slug              string `json:"slug"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return ModerationUser{}, errors.New("Kick user lookup returned an invalid response")
	}
	matches := make([]ModerationUser, 0, 1)
	for _, candidate := range payload.Data {
		if candidate.BroadcasterUserID > 0 && strings.EqualFold(candidate.Slug, username) {
			matches = append(matches, ModerationUser{ID: candidate.BroadcasterUserID, Username: candidate.Slug})
		}
	}
	switch len(matches) {
	case 0:
		return ModerationUser{}, fmt.Errorf("%w: %s", ErrModerationUserNotFound, username)
	case 1:
		return matches[0], nil
	default:
		return ModerationUser{}, &AmbiguousModerationUserError{Username: username}
	}
}

func (c ModerationClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c ModerationClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func moderationStatusError(response *http.Response, action bool) error {
	if response.StatusCode == http.StatusOK {
		return nil
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (HTTP 401); run: streamchat setup kick", ErrModerationAuthentication)
	case http.StatusForbidden:
		if !action || strings.Contains(strings.ToLower(response.Header.Get("WWW-Authenticate")), "insufficient_scope") {
			return fmt.Errorf("%w (HTTP 403)", ErrModerationScope)
		}
		return fmt.Errorf("%w (HTTP 403)", ErrModerationPermission)
	case http.StatusNotFound:
		if !action {
			return ErrModerationUserNotFound
		}
		return errors.New("Kick moderation API failed (HTTP 404)")
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w (HTTP 429)", ErrModerationRateLimit)
	default:
		name := "user lookup"
		if action {
			name = "moderation"
		}
		return fmt.Errorf("Kick %s API failed (HTTP %d)", name, response.StatusCode)
	}
}
