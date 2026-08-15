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

const ChannelWriteScope = "channel:write"

var (
	ErrChannelAuthentication  = errors.New("Kick channel authentication failed")
	ErrChannelReadPermission  = errors.New("Kick channel-read permission is missing; reauthorize Kick with channel:read")
	ErrChannelWritePermission = errors.New("Kick channel-update permission is missing; reauthorize Kick with channel:write")
	ErrChannelRateLimit       = errors.New("Kick channel API rate limit exceeded; try again later")
	ErrCategoryNotFound       = errors.New("Kick category not found")
)

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type ChannelStatus struct {
	Title       string
	Category    string
	ViewerCount int
	Live        bool
}

type AmbiguousCategoryError struct {
	Query      string
	Categories []Category
}

func (e *AmbiguousCategoryError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Multiple Kick categories match %q:", e.Query)
	limit := min(len(e.Categories), 5)
	for i := 0; i < limit; i++ {
		fmt.Fprintf(&b, "\n  %d. %s (%d)", i+1, e.Categories[i].Name, e.Categories[i].ID)
	}
	if len(e.Categories) > limit {
		fmt.Fprintf(&b, "\n  …and %d more", len(e.Categories)-limit)
	}
	b.WriteString("\nUse /category CATEGORY_ID.")
	return b.String()
}

type ChannelClient struct {
	HTTP                 *http.Client
	BaseURL, AccessToken string
}

func (c ChannelClient) UpdateTitle(ctx context.Context, title string) error {
	return c.patch(ctx, struct {
		StreamTitle string `json:"stream_title"`
	}{StreamTitle: title})
}

func (c ChannelClient) UpdateCategory(ctx context.Context, categoryID int64) error {
	return c.patch(ctx, struct {
		CategoryID int64 `json:"category_id"`
	}{CategoryID: categoryID})
}

func (c ChannelClient) GetStatus(ctx context.Context) (ChannelStatus, error) {
	if c.AccessToken == "" {
		return ChannelStatus{}, fmt.Errorf("%w; run: streamchat setup kick", ErrChannelAuthentication)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/channels", nil)
	if err != nil {
		return ChannelStatus{}, errors.New("create Kick channel-status request")
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return ChannelStatus{}, errors.New("Kick channel-status request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return ChannelStatus{}, fmt.Errorf("%w (HTTP 401); run: streamchat setup kick", ErrChannelAuthentication)
		case http.StatusForbidden:
			return ChannelStatus{}, fmt.Errorf("%w (HTTP 403); run: streamchat setup kick", ErrChannelReadPermission)
		case http.StatusTooManyRequests:
			return ChannelStatus{}, fmt.Errorf("%w (HTTP 429)", ErrChannelRateLimit)
		default:
			return ChannelStatus{}, fmt.Errorf("Kick channel-status API failed (HTTP %d)", response.StatusCode)
		}
	}
	var payload struct {
		Data []struct {
			StreamTitle string `json:"stream_title"`
			Category    struct {
				Name string `json:"name"`
			} `json:"category"`
			Stream *struct {
				IsLive      bool `json:"is_live"`
				ViewerCount int  `json:"viewer_count"`
			} `json:"stream"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil || len(payload.Data) == 0 {
		return ChannelStatus{}, errors.New("Kick channel-status API returned an invalid response")
	}
	channel := payload.Data[0]
	status := ChannelStatus{Title: channel.StreamTitle, Category: channel.Category.Name}
	if channel.Stream != nil {
		status.Live = channel.Stream.IsLive
		status.ViewerCount = channel.Stream.ViewerCount
	}
	return status, nil
}

func (c ChannelClient) patch(ctx context.Context, update any) error {
	body, err := json.Marshal(update)
	if err != nil {
		return err
	}
	response, err := c.do(ctx, http.MethodPatch, "/channels", bytes.NewReader(body), true)
	if response != nil {
		_ = response.Close()
	}
	return err
}

func (c ChannelClient) SearchCategories(ctx context.Context, query string) ([]Category, error) {
	path := "/categories?q=" + url.QueryEscape(query)
	body, err := c.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var response struct {
		Data []Category `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&response); err != nil {
		return nil, errors.New("Kick categories API returned an invalid response")
	}
	return response.Data, nil
}

func (c ChannelClient) GetCategory(ctx context.Context, categoryID int64) (Category, error) {
	body, err := c.do(ctx, http.MethodGet, "/categories/"+strconv.FormatInt(categoryID, 10), nil, false)
	if err != nil {
		return Category{}, err
	}
	defer body.Close()
	var response struct {
		Data Category `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&response); err != nil {
		return Category{}, errors.New("Kick categories API returned an invalid response")
	}
	if response.Data.ID <= 0 || response.Data.Name == "" {
		return Category{}, errors.New("Kick categories API returned an invalid category")
	}
	return response.Data, nil
}

func (c ChannelClient) ResolveCategory(ctx context.Context, argument string) (Category, error) {
	argument = strings.TrimSpace(argument)
	if id, err := strconv.ParseInt(argument, 10, 64); err == nil {
		if id <= 0 {
			return Category{}, errors.New("Kick category ID must be a positive integer")
		}
		return c.GetCategory(ctx, id)
	}
	categories, err := c.SearchCategories(ctx, argument)
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
	if len(categories) == 1 {
		return categories[0], nil
	}
	return Category{}, &AmbiguousCategoryError{Query: argument, Categories: categories}
}

func (c ChannelClient) do(ctx context.Context, method, path string, body io.Reader, update bool) (io.ReadCloser, error) {
	if c.AccessToken == "" {
		return nil, fmt.Errorf("%w; run: streamchat setup kick", ErrChannelAuthentication)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Kick channel API request failed: %w", err)
	}
	if (update && response.StatusCode == http.StatusNoContent) || (!update && response.StatusCode == http.StatusOK) {
		return response.Body, nil
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("%w (HTTP 401); run: streamchat setup kick", ErrChannelAuthentication)
	case http.StatusForbidden:
		if update {
			return nil, fmt.Errorf("%w (HTTP 403); run: streamchat setup kick", ErrChannelWritePermission)
		}
		return nil, errors.New("Kick categories API access was forbidden (HTTP 403)")
	case http.StatusNotFound:
		if !update {
			return nil, ErrCategoryNotFound
		}
		return nil, errors.New("Kick channel API failed (HTTP 404)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w (HTTP 429)", ErrChannelRateLimit)
	default:
		name := "categories"
		if update {
			name = "channel"
		}
		return nil, fmt.Errorf("Kick %s API failed (HTTP %d)", name, response.StatusCode)
	}
}
