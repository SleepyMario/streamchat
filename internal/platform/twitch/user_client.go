package twitch

import (
	"context"
	"io"
	"net/http"
	"sync"
)

// UserClient serializes access-token use and owns the single refresh/retry path
// shared by Twitch chat, status, and channel controls in an interactive client.
type UserClient struct {
	API    *API
	mu     sync.Mutex
	scopes []string
}

func NewUserClient(api *API, scopes []string) *UserClient {
	return &UserClient{API: api, scopes: append([]string(nil), scopes...)}
}

func (c *UserClient) RequireScopes(required ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return RequireScopes(c.scopes, required...)
}

func (c *UserClient) Do(ctx context.Context, required []string, build func(string) (*http.Request, error)) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.API == nil {
		return nil, ErrChatAuthentication
	}
	if err := RequireScopes(c.scopes, required...); err != nil {
		return nil, err
	}
	response, err := c.do(ctx, build)
	if err != nil || response.StatusCode != http.StatusUnauthorized || c.API.RefreshToken == "" {
		return response, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	token, err := c.API.Refresh(ctx, c.API.RefreshToken)
	if err != nil {
		return nil, err
	}
	c.API.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		c.API.RefreshToken = token.RefreshToken
	}
	if len(token.Scopes) > 0 {
		c.scopes = append(c.scopes[:0], token.Scopes...)
	} else {
		identity, validateErr := c.API.ValidateToken(ctx)
		if validateErr != nil {
			return nil, validateErr
		}
		c.scopes = append(c.scopes[:0], identity.Scopes...)
	}
	if c.API.OnToken != nil {
		if err = c.API.OnToken(token); err != nil {
			return nil, err
		}
	}
	if err = RequireScopes(c.scopes, required...); err != nil {
		return nil, err
	}
	return c.do(ctx, build)
}

func (c *UserClient) do(ctx context.Context, build func(string) (*http.Request, error)) (*http.Response, error) {
	request, err := build(c.API.AccessToken)
	if err != nil {
		return nil, err
	}
	return c.API.httpClient().Do(request.WithContext(ctx))
}
