package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	YouTube           YouTube `json:"youtube"`
	Kick              Kick    `json:"kick"`
	LogFile           string  `json:"log_file,omitempty"`
	Timestamps        bool    `json:"timestamps,omitempty"`
	NoColor           bool    `json:"no_color,omitempty"`
	QueueSize         int     `json:"queue_size,omitempty"`
	DuplicateCapacity int     `json:"duplicate_capacity,omitempty"`
}
type YouTube struct {
	APIKey      string `json:"api_key,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	VideoID     string `json:"video_id,omitempty"`
	BaseURL     string `json:"-"`
}
type Kick struct {
	ClientID      string        `json:"client_id,omitempty"`
	ClientSecret  string        `json:"client_secret,omitempty"`
	AccessToken   string        `json:"access_token,omitempty"`
	BroadcasterID string        `json:"broadcaster_id,omitempty"`
	WebhookURL    string        `json:"webhook_url,omitempty"`
	Listen        string        `json:"listen,omitempty"`
	PublicKeyPEM  string        `json:"public_key_pem,omitempty"`
	APIBaseURL    string        `json:"-"`
	MaxBodyBytes  int64         `json:"max_body_bytes,omitempty"`
	MaxAge        time.Duration `json:"-"`
}

func Defaults() Config {
	return Config{YouTube: YouTube{BaseURL: "https://www.googleapis.com/youtube/v3"}, Kick: Kick{Listen: "127.0.0.1:8788", APIBaseURL: "https://api.kick.com/public/v1", MaxBodyBytes: 1 << 20, MaxAge: 5 * time.Minute}, QueueSize: 256, DuplicateCapacity: 10000}
}
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "streamchat", "config.json")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "streamchat", "config.json")
}
func Load(path string) (Config, error) {
	c := Defaults()
	if path == "" {
		path = DefaultPath()
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, fmt.Errorf("parse config: %w", e)
	}
	applyDefaults(&c)
	return c, nil
}
func applyDefaults(c *Config) {
	d := Defaults()
	if c.YouTube.BaseURL == "" {
		c.YouTube.BaseURL = d.YouTube.BaseURL
	}
	if c.Kick.Listen == "" {
		c.Kick.Listen = d.Kick.Listen
	}
	if c.Kick.APIBaseURL == "" {
		c.Kick.APIBaseURL = d.Kick.APIBaseURL
	}
	if c.Kick.MaxBodyBytes == 0 {
		c.Kick.MaxBodyBytes = d.Kick.MaxBodyBytes
	}
	if c.Kick.MaxAge == 0 {
		c.Kick.MaxAge = d.Kick.MaxAge
	}
	if c.QueueSize == 0 {
		c.QueueSize = d.QueueSize
	}
	if c.DuplicateCapacity == 0 {
		c.DuplicateCapacity = d.DuplicateCapacity
	}
}
func ApplyEnv(c *Config, get func(string) string) {
	set := func(k string, p *string) {
		if v := get(k); v != "" {
			*p = v
		}
	}
	set("STREAMCHAT_YOUTUBE_API_KEY", &c.YouTube.APIKey)
	set("STREAMCHAT_YOUTUBE_ACCESS_TOKEN", &c.YouTube.AccessToken)
	set("STREAMCHAT_YOUTUBE_VIDEO_ID", &c.YouTube.VideoID)
	set("STREAMCHAT_KICK_CLIENT_ID", &c.Kick.ClientID)
	set("STREAMCHAT_KICK_CLIENT_SECRET", &c.Kick.ClientSecret)
	set("STREAMCHAT_KICK_ACCESS_TOKEN", &c.Kick.AccessToken)
	set("STREAMCHAT_KICK_BROADCASTER_ID", &c.Kick.BroadcasterID)
	set("STREAMCHAT_KICK_WEBHOOK_URL", &c.Kick.WebhookURL)
	set("STREAMCHAT_KICK_LISTEN", &c.Kick.Listen)
	set("STREAMCHAT_LOG_FILE", &c.LogFile)
}
func (c Config) Validate(mode string) error {
	if c.QueueSize < 1 || c.QueueSize > 65536 {
		return errors.New("queue_size must be between 1 and 65536")
	}
	if c.DuplicateCapacity < 1 || c.DuplicateCapacity > 1000000 {
		return errors.New("duplicate_capacity must be between 1 and 1000000")
	}
	if c.Kick.Listen == "" {
		return errors.New("kick.listen is required")
	}
	host, _, err := net.SplitHostPort(c.Kick.Listen)
	if err != nil {
		return errors.New("kick.listen must be a host:port address")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("kick.listen must use localhost or a loopback IP by default")
	}
	if c.Kick.BroadcasterID != "" {
		if n, err := strconv.ParseInt(c.Kick.BroadcasterID, 10, 64); err != nil || n <= 0 {
			return errors.New("kick.broadcaster_id must be a positive integer")
		}
	}
	if c.Kick.WebhookURL != "" {
		u, err := url.Parse(c.Kick.WebhookURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("kick.webhook_url must be an absolute HTTPS URL")
		}
	}
	if mode == "demo" || mode == "check" {
		return nil
	}
	if mode == "youtube" && (c.YouTube.VideoID == "" || (c.YouTube.APIKey == "" && c.YouTube.AccessToken == "")) {
		return errors.New("YouTube video ID and API key or access token are required")
	}
	return nil
}
func Redact(v string) string {
	if v == "" {
		return "<unset>"
	}
	if len(v) < 8 {
		return "<redacted>"
	}
	return v[:3] + "…<redacted>"
}
