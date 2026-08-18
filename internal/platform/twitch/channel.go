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

var (
	ErrChannelAuthentication = errors.New("Twitch channel authentication failed; run: streamchat setup twitch")
	ErrChannelRateLimit      = errors.New("Twitch channel API rate limit exceeded; try again shortly")
	ErrChannelNotFound       = errors.New("Twitch channel was not found")
	ErrChannelOwnership      = errors.New("Twitch title/category updates require the authenticated user to own the selected channel")
	ErrCategoryNotFound      = errors.New("Twitch category was not found")
)

type ChannelStatus struct {
	Title       string
	CategoryID  string
	Category    string
	ViewerCount int
	Live        bool
}

type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AmbiguousCategoryError struct {
	Query      string
	Categories []Category
}

func (e *AmbiguousCategoryError) Error() string {
	var result strings.Builder
	fmt.Fprintf(&result, "No unique exact Twitch category matches %q.", e.Query)
	limit := min(len(e.Categories), 5)
	for index := 0; index < limit; index++ {
		fmt.Fprintf(&result, "\n  %d. %s (%s)", index+1, e.Categories[index].Name, e.Categories[index].ID)
	}
	if len(e.Categories) > limit {
		fmt.Fprintf(&result, "\n  …and %d more", len(e.Categories)-limit)
	}
	result.WriteString("\nUse /category CATEGORY_ID.")
	return result.String()
}

type ChannelClient struct {
	Auth                  *UserClient
	BroadcasterID, UserID string
}

func NewChannelClient(auth *UserClient, broadcasterID, userID string) *ChannelClient {
	return &ChannelClient{Auth: auth, BroadcasterID: broadcasterID, UserID: userID}
}

func (c *ChannelClient) GetStatus(ctx context.Context) (ChannelStatus, error) {
	if c.Auth == nil || c.BroadcasterID == "" {
		return ChannelStatus{}, ErrChannelNotFound
	}
	channel, err := c.channelInformation(ctx)
	if err != nil {
		return ChannelStatus{}, err
	}
	status := ChannelStatus{Title: channel.Title, CategoryID: channel.GameID, Category: channel.GameName}
	response, err := c.Auth.Do(ctx, nil, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodGet, "/streams?user_id="+url.QueryEscape(c.BroadcasterID), nil, accessToken)
	})
	if err != nil {
		return ChannelStatus{}, errors.New("Twitch stream-status request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return ChannelStatus{}, channelAPIError("stream status", response.StatusCode)
	}
	var payload struct {
		Data []struct {
			UserID      string `json:"user_id"`
			ViewerCount int    `json:"viewer_count"`
			Type        string `json:"type"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return ChannelStatus{}, errors.New("Twitch stream-status API returned an invalid response")
	}
	if len(payload.Data) == 0 {
		return status, nil
	}
	stream := payload.Data[0]
	if stream.UserID != c.BroadcasterID {
		return ChannelStatus{}, errors.New("Twitch stream-status API returned an unexpected channel")
	}
	status.Live = stream.Type == "live"
	if status.Live {
		status.ViewerCount = stream.ViewerCount
	}
	return status, nil
}

func (c *ChannelClient) UpdateTitle(ctx context.Context, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("Twitch title must not be empty")
	}
	return c.patch(ctx, map[string]string{"title": title})
}

func (c *ChannelClient) UpdateCategory(ctx context.Context, gameID string) error {
	if strings.TrimSpace(gameID) == "" {
		return errors.New("Twitch category ID must not be empty")
	}
	return c.patch(ctx, map[string]string{"game_id": gameID})
}

func (c *ChannelClient) RequireManagement() error {
	if c.Auth == nil || c.BroadcasterID == "" || c.UserID == "" || c.UserID != c.BroadcasterID {
		return ErrChannelOwnership
	}
	return c.Auth.RequireScopes(ManageBroadcastScope)
}

func (c *ChannelClient) ResolveCategory(ctx context.Context, argument string) (Category, error) {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return Category{}, ErrCategoryNotFound
	}
	if numeric(argument) {
		id, err := strconv.ParseUint(argument, 10, 64)
		if err != nil || id == 0 {
			return Category{}, errors.New("Twitch category ID must be a positive integer")
		}
		return c.getCategory(ctx, argument)
	}
	categories, err := c.searchCategories(ctx, argument)
	if err != nil {
		return Category{}, err
	}
	if len(categories) == 0 {
		return Category{}, fmt.Errorf("%w for %q", ErrCategoryNotFound, argument)
	}
	exact := make([]Category, 0, 1)
	for _, category := range categories {
		if strings.EqualFold(strings.TrimSpace(category.Name), argument) {
			exact = append(exact, category)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return Category{}, &AmbiguousCategoryError{Query: argument, Categories: exact}
	}
	return Category{}, &AmbiguousCategoryError{Query: argument, Categories: categories}
}

type channelInformation struct {
	BroadcasterID string `json:"broadcaster_id"`
	GameID        string `json:"game_id"`
	GameName      string `json:"game_name"`
	Title         string `json:"title"`
}

func (c *ChannelClient) channelInformation(ctx context.Context) (channelInformation, error) {
	response, err := c.Auth.Do(ctx, nil, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodGet, "/channels?broadcaster_id="+url.QueryEscape(c.BroadcasterID), nil, accessToken)
	})
	if err != nil {
		return channelInformation{}, errors.New("Twitch channel-status request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return channelInformation{}, channelAPIError("channel status", response.StatusCode)
	}
	var payload struct {
		Data []channelInformation `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return channelInformation{}, errors.New("Twitch channel-status API returned an invalid response")
	}
	if len(payload.Data) != 1 || payload.Data[0].BroadcasterID != c.BroadcasterID {
		return channelInformation{}, ErrChannelNotFound
	}
	return payload.Data[0], nil
}

func (c *ChannelClient) getCategory(ctx context.Context, id string) (Category, error) {
	categories, err := c.getCategories(ctx, "/games?id="+url.QueryEscape(id))
	if err != nil {
		return Category{}, err
	}
	if len(categories) != 1 || categories[0].ID == "" || categories[0].Name == "" {
		return Category{}, fmt.Errorf("%w for ID %s", ErrCategoryNotFound, id)
	}
	return categories[0], nil
}

func (c *ChannelClient) searchCategories(ctx context.Context, query string) ([]Category, error) {
	return c.getCategories(ctx, "/search/categories?query="+url.QueryEscape(query)+"&first=100")
}

func (c *ChannelClient) getCategories(ctx context.Context, path string) ([]Category, error) {
	response, err := c.Auth.Do(ctx, nil, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodGet, path, nil, accessToken)
	})
	if err != nil {
		return nil, errors.New("Twitch category request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, channelAPIError("category", response.StatusCode)
	}
	var payload struct {
		Data []Category `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, errors.New("Twitch category API returned an invalid response")
	}
	return payload.Data, nil
}

func (c *ChannelClient) patch(ctx context.Context, update map[string]string) error {
	if err := c.RequireManagement(); err != nil {
		return err
	}
	body, err := json.Marshal(update)
	if err != nil {
		return err
	}
	response, err := c.Auth.Do(ctx, []string{ManageBroadcastScope}, func(accessToken string) (*http.Request, error) {
		return c.request(http.MethodPatch, "/channels?broadcaster_id="+url.QueryEscape(c.BroadcasterID), bytes.NewReader(body), accessToken)
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w; verify channel ownership and rerun: streamchat setup twitch", ErrChannelAuthentication)
	}
	return channelAPIError("channel update", response.StatusCode)
}

func (c *ChannelClient) request(method, path string, body io.Reader, accessToken string) (*http.Request, error) {
	if c.Auth == nil || c.Auth.API == nil {
		return nil, ErrChannelAuthentication
	}
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

func channelAPIError(operation string, status int) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrChannelAuthentication
	case http.StatusTooManyRequests:
		return ErrChannelRateLimit
	case http.StatusNotFound:
		return ErrChannelNotFound
	case http.StatusBadRequest, http.StatusForbidden:
		return fmt.Errorf("Twitch %s request was rejected (HTTP %d)", operation, status)
	default:
		return fmt.Errorf("Twitch %s API failed (HTTP %d)", operation, status)
	}
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
