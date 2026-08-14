package platform

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
)

type Definition struct {
	Name, DisplayName, TargetPrompt string
	Configured                      func(config.Config) bool
	Target                          func(config.Config) string
	SetTarget                       func(*config.Config, string) error
	Build                           func(*config.Config) (chat.Adapter, error)
}

type Registry struct{ definitions []Definition }

func Default() Registry {
	return Registry{definitions: []Definition{
		{Name: "youtube", DisplayName: "YouTube", TargetPrompt: "YouTube live URL or video ID", Configured: func(c config.Config) bool { return c.YouTube.APIKey != "" || c.YouTube.AccessToken != "" }, Target: func(c config.Config) string { return c.YouTube.VideoID }, SetTarget: func(c *config.Config, v string) error {
			id, e := youtube.ParseVideoID(v)
			if e == nil {
				c.YouTube.VideoID = id
			}
			return e
		}, Build: func(c *config.Config) (chat.Adapter, error) {
			if err := c.Validate("youtube"); err != nil {
				return nil, err
			}
			return youtube.New(nil, c.YouTube.BaseURL, c.YouTube.APIKey, c.YouTube.AccessToken, c.YouTube.VideoID), nil
		}},
		{Name: "kick", DisplayName: "Kick", Configured: func(c config.Config) bool { return c.Kick.AccessToken != "" && c.Kick.WebhookURL != "" }, Target: func(c config.Config) string { return c.Kick.BroadcasterID }, SetTarget: func(c *config.Config, v string) error { c.Kick.BroadcasterID = v; return nil }, Build: func(c *config.Config) (chat.Adapter, error) {
			k, e := kick.ParsePublicKey([]byte(kick.OfficialPublicKeyPEM))
			if e != nil {
				return nil, e
			}
			return kick.NewServer(c.Kick.Listen, k, nil), nil
		}},
		{Name: "twitch", DisplayName: "Twitch", TargetPrompt: "Twitch channel", Configured: func(c config.Config) bool { return c.Twitch.ClientID != "" && c.Twitch.AccessToken != "" }, Target: func(c config.Config) string { return c.Twitch.Channel }, SetTarget: func(c *config.Config, v string) error {
			ch, e := twitch.ParseChannel(v)
			if e == nil {
				c.Twitch.Channel = ch
			}
			return e
		}, Build: func(c *config.Config) (chat.Adapter, error) {
			if err := c.Validate("twitch"); err != nil {
				return nil, err
			}
			api := &twitch.API{HTTP: &http.Client{Timeout: 20 * time.Second}, APIBaseURL: c.Twitch.APIBaseURL, OAuthBaseURL: c.Twitch.OAuthBaseURL, ClientID: c.Twitch.ClientID, ClientSecret: c.Twitch.ClientSecret, AccessToken: c.Twitch.AccessToken, RefreshToken: c.Twitch.RefreshToken}
			api.OnToken = func(tok twitch.Token) error {
				c.Twitch.AccessToken = tok.AccessToken
				if tok.RefreshToken != "" {
					c.Twitch.RefreshToken = tok.RefreshToken
				}
				c.Twitch.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
				if os.Getenv("STREAMCHAT_TWITCH_ACCESS_TOKEN") != "" || c.Path == "" {
					return nil
				}
				disk, e := config.Load(c.Path)
				if e != nil {
					return e
				}
				disk.Twitch.AccessToken = c.Twitch.AccessToken
				disk.Twitch.RefreshToken = c.Twitch.RefreshToken
				disk.Twitch.TokenExpiry = c.Twitch.TokenExpiry
				return config.Save(c.Path, disk)
			}
			return twitch.New(api, c.Twitch.WebSocketURL, c.Twitch.Channel, c.Twitch.UserID), nil
		}},
	}}
}

func (r Registry) Definitions() []Definition { return append([]Definition(nil), r.definitions...) }
func (r Registry) Find(name string) (Definition, bool) {
	for _, d := range r.definitions {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}

// Select returns configured adapters in stable registry order. Explicit names
// restrict activation; otherwise every configured platform with a target runs.
func (r Registry) Select(c *config.Config, explicit map[string]bool) ([]chat.Adapter, error) {
	var out []chat.Adapter
	for _, d := range r.definitions {
		if len(explicit) > 0 && !explicit[d.Name] {
			continue
		}
		if !d.Configured(*c) {
			if explicit[d.Name] {
				return nil, errors.New(d.DisplayName + " is not configured. Run: streamchat setup " + d.Name)
			}
			continue
		}
		if d.Target(*c) == "" {
			continue
		}
		a, e := d.Build(c)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, nil
}
