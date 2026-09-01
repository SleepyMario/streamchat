package config

import (
	"bytes"
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

	"github.com/SleepyMario/streamchat/internal/chattercolor"
)

type Config struct {
	Path              string  `json:"-"`
	YouTube           YouTube `json:"youtube"`
	Kick              Kick    `json:"kick"`
	Twitch            Twitch  `json:"twitch"`
	Server            Server  `json:"server"`
	Client            Client  `json:"client"`
	Storage           Storage `json:"storage"`
	Emotes            Emotes  `json:"emotes"`
	Bot               Bot     `json:"bot"`
	ChatColorMode     string  `json:"chat_color_mode,omitempty"`
	RelayAuthToken    string  `json:"relay_auth_token,omitempty"`
	LogFile           string  `json:"log_file,omitempty"`
	Timestamps        bool    `json:"timestamps,omitempty"`
	NoColor           bool    `json:"no_color,omitempty"`
	QueueSize         int     `json:"queue_size,omitempty"`
	DuplicateCapacity int     `json:"duplicate_capacity,omitempty"`
}

type YouTube struct {
	APIKey       string    `json:"api_key,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenExpiry  time.Time `json:"token_expiry,omitempty"`
	RedirectURI  string    `json:"redirect_uri,omitempty"`
	// VideoID is retained for compatibility, but is normally a per-run target.
	VideoID string `json:"video_id,omitempty"`
	BaseURL string `json:"-"`
}

type Kick struct {
	ClientID      string        `json:"client_id,omitempty"`
	ClientSecret  string        `json:"client_secret,omitempty"`
	AccessToken   string        `json:"access_token,omitempty"`
	RefreshToken  string        `json:"refresh_token,omitempty"`
	TokenExpiry   time.Time     `json:"token_expiry,omitempty"`
	BroadcasterID string        `json:"broadcaster_id,omitempty"`
	WebhookURL    string        `json:"webhook_url,omitempty"`
	RedirectURI   string        `json:"redirect_uri,omitempty"`
	Listen        string        `json:"listen,omitempty"`
	PublicKeyPEM  string        `json:"public_key_pem,omitempty"`
	APIBaseURL    string        `json:"-"`
	OAuthBaseURL  string        `json:"-"`
	MaxBodyBytes  int64         `json:"max_body_bytes,omitempty"`
	MaxAge        time.Duration `json:"-"`
}

type Twitch struct {
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenExpiry  time.Time `json:"token_expiry,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	UserLogin    string    `json:"user_login,omitempty"`
	RedirectURI  string    `json:"redirect_uri,omitempty"`
	// Channel is a convenient default target and may be overridden per run.
	Channel      string `json:"channel,omitempty"`
	APIBaseURL   string `json:"-"`
	OAuthBaseURL string `json:"-"`
	WebSocketURL string `json:"-"`
}

type Server struct {
	Listen        string `json:"listen,omitempty"`
	WebSocketPath string `json:"websocket_path,omitempty"`
}

type Client struct {
	ServerURL string `json:"server_url,omitempty"`
}

type Storage struct {
	SQLitePath string `json:"sqlite_path,omitempty"`
}

type Emotes struct {
	Mode  string `json:"mode,omitempty"`
	Debug bool   `json:"debug,omitempty"`
}

type Bot struct {
	Enabled           bool        `json:"enabled,omitempty"`
	ShowChat          bool        `json:"show_chat,omitempty"`
	CommandsReply     string      `json:"commands_reply,omitempty"`
	CooldownSeconds   int         `json:"cooldown_seconds,omitempty"`
	DisabledPlatforms []string    `json:"disabled_platforms,omitempty"`
	GUIListen         string      `json:"gui_listen,omitempty"`
	GUIPassword       string      `json:"gui_password,omitempty"`
	StreamProbeURLEnv string      `json:"stream_probe_url_env,omitempty"`
	MetricsURLEnv     string      `json:"mediamtx_metrics_url_env,omitempty"`
	MediaPath         string      `json:"media_path,omitempty"`
	FFprobe           string      `json:"ffprobe,omitempty"`
	Discord           DiscordBot  `json:"discord,omitempty"`
	Kick              BotAccount  `json:"kick,omitempty"`
	Twitch            BotAccount  `json:"twitch,omitempty"`
	Subtitles         SubtitleBot `json:"subtitles,omitempty"`
}

// SubtitleBot configures the bot-owned lifecycle for a temporary RunPod GPU
// subtitle worker. Provider and worker credentials remain environment-only.
type SubtitleBot struct {
	Enabled           bool     `json:"enabled,omitempty"`
	APIBaseURL        string   `json:"api_base_url,omitempty"`
	APIKeyEnv         string   `json:"api_key_env,omitempty"`
	Image             string   `json:"image,omitempty"`
	GPUTypeIDs        []string `json:"gpu_type_ids,omitempty"`
	CloudType         string   `json:"cloud_type,omitempty"`
	Model             string   `json:"model,omitempty"`
	AcceptedLanguages string   `json:"accepted_languages,omitempty"`
	WorkerPort        int      `json:"worker_port,omitempty"`
	ContainerDiskGB   int      `json:"container_disk_gb,omitempty"`
	ReadyMinutes      int      `json:"ready_minutes,omitempty"`
	MaxMinutes        int      `json:"max_minutes,omitempty"`
	HeartbeatSeconds  int      `json:"heartbeat_seconds,omitempty"`
	MaxCostPerHour    float64  `json:"max_cost_per_hour,omitempty"`
	StatePath         string   `json:"state_path,omitempty"`
}

// DiscordBot configures one low-privilege Discord bot destination. The bot
// token itself remains in the named service environment variable.
type DiscordBot struct {
	Enabled       bool   `json:"enabled,omitempty"`
	ChannelID     string `json:"channel_id,omitempty"`
	Message       string `json:"message,omitempty"`
	TokenEnv      string `json:"token_env,omitempty"`
	Confirmations int    `json:"confirmations,omitempty"`
}

// BotAccount holds a separate chat identity. Channel targets continue to come
// from the normal Kick and Twitch sections.
type BotAccount struct {
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenExpiry  time.Time `json:"token_expiry,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	UserLogin    string    `json:"user_login,omitempty"`
}

const legacyYouTubeRedirectURI = "http://localhost:8791/oauth/youtube/callback"

func Defaults() Config {
	return Config{
		YouTube:       YouTube{BaseURL: "https://youtube.googleapis.com/youtube/v3", RedirectURI: "http://127.0.0.1:8791"},
		Kick:          Kick{Listen: "127.0.0.1:8788", RedirectURI: "http://localhost:8789/oauth/kick/callback", APIBaseURL: "https://api.kick.com/public/v1", OAuthBaseURL: "https://id.kick.com", MaxBodyBytes: 1 << 20, MaxAge: 5 * time.Minute},
		Twitch:        Twitch{RedirectURI: "http://localhost:8790/oauth/twitch/callback", APIBaseURL: "https://api.twitch.tv/helix", OAuthBaseURL: "https://id.twitch.tv/oauth2", WebSocketURL: "wss://eventsub.wss.twitch.tv/ws"},
		Server:        Server{Listen: "127.0.0.1:8788", WebSocketPath: "/relay"},
		Storage:       Storage{SQLitePath: "/var/lib/streamchat/streamchat.db"},
		Emotes:        Emotes{Mode: "auto"},
		Bot:           Bot{CommandsReply: "Commands: !commands", CooldownSeconds: 5, StreamProbeURLEnv: "STREAMCHAT_MEDIA_INPUT_URL", MetricsURLEnv: "STREAMCHAT_MEDIAMTX_METRICS_URL", MediaPath: "pc/stream", FFprobe: "/usr/bin/ffprobe", Discord: DiscordBot{Message: "@everyone Sleepymario has started streaming on", TokenEnv: "STREAMCHAT_DISCORD_BOT_TOKEN", Confirmations: 2}, Subtitles: SubtitleBot{APIBaseURL: "https://api.runpod.io/v2", APIKeyEnv: "RUNPOD_API_KEY", Image: "sleepiestmario/gpu-subtitle-worker:0.1.0-test2", GPUTypeIDs: []string{"NVIDIA RTX A4000", "NVIDIA RTX A4500", "NVIDIA RTX 4000 Ada Generation"}, CloudType: "SECURE", Model: "large-v3", AcceptedLanguages: "en,nl,de,zh,ko,vi,ja", WorkerPort: 8000, ContainerDiskGB: 30, ReadyMinutes: 15, MaxMinutes: 360, HeartbeatSeconds: 120, MaxCostPerHour: 0.35, StatePath: "/var/lib/streamchat/subtitle-session.json"}},
		ChatColorMode: chattercolor.ModeLine,
		QueueSize:     256, DuplicateCapacity: 10000,
	}
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
	c.Path = path
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
	c.Path = path
	return c, nil
}

func applyDefaults(c *Config) {
	d := Defaults()
	if c.YouTube.BaseURL == "" {
		c.YouTube.BaseURL = d.YouTube.BaseURL
	}
	if c.YouTube.RedirectURI == "" || c.YouTube.RedirectURI == legacyYouTubeRedirectURI {
		c.YouTube.RedirectURI = d.YouTube.RedirectURI
	}
	if c.Kick.Listen == "" {
		c.Kick.Listen = d.Kick.Listen
	}
	if c.Kick.RedirectURI == "" {
		c.Kick.RedirectURI = d.Kick.RedirectURI
	}
	if c.Kick.APIBaseURL == "" {
		c.Kick.APIBaseURL = d.Kick.APIBaseURL
	}
	if c.Kick.OAuthBaseURL == "" {
		c.Kick.OAuthBaseURL = d.Kick.OAuthBaseURL
	}
	if c.Kick.MaxBodyBytes == 0 {
		c.Kick.MaxBodyBytes = d.Kick.MaxBodyBytes
	}
	if c.Kick.MaxAge == 0 {
		c.Kick.MaxAge = d.Kick.MaxAge
	}
	if c.Twitch.RedirectURI == "" {
		c.Twitch.RedirectURI = d.Twitch.RedirectURI
	}
	if c.Twitch.APIBaseURL == "" {
		c.Twitch.APIBaseURL = d.Twitch.APIBaseURL
	}
	if c.Twitch.OAuthBaseURL == "" {
		c.Twitch.OAuthBaseURL = d.Twitch.OAuthBaseURL
	}
	if c.Twitch.WebSocketURL == "" {
		c.Twitch.WebSocketURL = d.Twitch.WebSocketURL
	}
	if c.Server.Listen == "" {
		c.Server.Listen = d.Server.Listen
	}
	if c.Server.WebSocketPath == "" {
		c.Server.WebSocketPath = d.Server.WebSocketPath
	}
	if c.Storage.SQLitePath == "" {
		c.Storage.SQLitePath = d.Storage.SQLitePath
	}
	if c.Emotes.Mode == "" {
		c.Emotes.Mode = d.Emotes.Mode
	}
	if c.Bot.CommandsReply == "" {
		c.Bot.CommandsReply = d.Bot.CommandsReply
	}
	if c.Bot.CooldownSeconds == 0 {
		c.Bot.CooldownSeconds = d.Bot.CooldownSeconds
	}
	if c.Bot.StreamProbeURLEnv == "" {
		c.Bot.StreamProbeURLEnv = d.Bot.StreamProbeURLEnv
	}
	if c.Bot.MetricsURLEnv == "" {
		c.Bot.MetricsURLEnv = d.Bot.MetricsURLEnv
	}
	if c.Bot.MediaPath == "" {
		c.Bot.MediaPath = d.Bot.MediaPath
	}
	if c.Bot.FFprobe == "" {
		c.Bot.FFprobe = d.Bot.FFprobe
	}
	if c.Bot.Discord.Message == "" {
		c.Bot.Discord.Message = d.Bot.Discord.Message
	}
	if c.Bot.Discord.TokenEnv == "" {
		c.Bot.Discord.TokenEnv = d.Bot.Discord.TokenEnv
	}
	if c.Bot.Discord.Confirmations == 0 {
		c.Bot.Discord.Confirmations = d.Bot.Discord.Confirmations
	}
	if c.Bot.Subtitles.APIBaseURL == "" {
		c.Bot.Subtitles.APIBaseURL = d.Bot.Subtitles.APIBaseURL
	}
	if c.Bot.Subtitles.APIKeyEnv == "" {
		c.Bot.Subtitles.APIKeyEnv = d.Bot.Subtitles.APIKeyEnv
	}
	if c.Bot.Subtitles.Image == "" {
		c.Bot.Subtitles.Image = d.Bot.Subtitles.Image
	}
	if len(c.Bot.Subtitles.GPUTypeIDs) == 0 {
		c.Bot.Subtitles.GPUTypeIDs = append([]string(nil), d.Bot.Subtitles.GPUTypeIDs...)
	}
	if c.Bot.Subtitles.CloudType == "" {
		c.Bot.Subtitles.CloudType = d.Bot.Subtitles.CloudType
	}
	if c.Bot.Subtitles.Model == "" {
		c.Bot.Subtitles.Model = d.Bot.Subtitles.Model
	}
	if c.Bot.Subtitles.AcceptedLanguages == "" {
		c.Bot.Subtitles.AcceptedLanguages = d.Bot.Subtitles.AcceptedLanguages
	}
	if c.Bot.Subtitles.WorkerPort == 0 {
		c.Bot.Subtitles.WorkerPort = d.Bot.Subtitles.WorkerPort
	}
	if c.Bot.Subtitles.ContainerDiskGB == 0 {
		c.Bot.Subtitles.ContainerDiskGB = d.Bot.Subtitles.ContainerDiskGB
	}
	if c.Bot.Subtitles.ReadyMinutes == 0 {
		c.Bot.Subtitles.ReadyMinutes = d.Bot.Subtitles.ReadyMinutes
	}
	if c.Bot.Subtitles.MaxMinutes == 0 {
		c.Bot.Subtitles.MaxMinutes = d.Bot.Subtitles.MaxMinutes
	}
	if c.Bot.Subtitles.HeartbeatSeconds == 0 {
		c.Bot.Subtitles.HeartbeatSeconds = d.Bot.Subtitles.HeartbeatSeconds
	}
	if c.Bot.Subtitles.MaxCostPerHour == 0 {
		c.Bot.Subtitles.MaxCostPerHour = d.Bot.Subtitles.MaxCostPerHour
	}
	if c.Bot.Subtitles.StatePath == "" {
		c.Bot.Subtitles.StatePath = d.Bot.Subtitles.StatePath
	}
	if c.ChatColorMode == "" {
		c.ChatColorMode = d.ChatColorMode
	}
	if c.QueueSize == 0 {
		c.QueueSize = d.QueueSize
	}
	if c.DuplicateCapacity == 0 {
		c.DuplicateCapacity = d.DuplicateCapacity
	}
}

// Save writes the complete configuration using atomic replacement where the
// filesystem supports rename. Both the directory and file are private.
func Save(path string, c Config) error {
	if path == "" {
		path = DefaultPath()
	}
	applyDefaults(&c)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	_ = os.Chmod(filepath.Dir(path), 0700)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("secure config: %w", err)
	}
	return nil
}

func ApplyEnv(c *Config, get func(string) string) {
	set := func(k string, p *string) {
		if v := get(k); v != "" {
			*p = v
		}
	}
	set("STREAMCHAT_YOUTUBE_API_KEY", &c.YouTube.APIKey)
	set("STREAMCHAT_YOUTUBE_CLIENT_ID", &c.YouTube.ClientID)
	set("STREAMCHAT_YOUTUBE_CLIENT_SECRET", &c.YouTube.ClientSecret)
	set("STREAMCHAT_YOUTUBE_ACCESS_TOKEN", &c.YouTube.AccessToken)
	set("STREAMCHAT_YOUTUBE_REFRESH_TOKEN", &c.YouTube.RefreshToken)
	set("STREAMCHAT_YOUTUBE_REDIRECT_URI", &c.YouTube.RedirectURI)
	set("STREAMCHAT_YOUTUBE_VIDEO_ID", &c.YouTube.VideoID)
	set("STREAMCHAT_KICK_CLIENT_ID", &c.Kick.ClientID)
	set("STREAMCHAT_KICK_CLIENT_SECRET", &c.Kick.ClientSecret)
	set("STREAMCHAT_KICK_ACCESS_TOKEN", &c.Kick.AccessToken)
	set("STREAMCHAT_KICK_REFRESH_TOKEN", &c.Kick.RefreshToken)
	set("STREAMCHAT_KICK_BROADCASTER_ID", &c.Kick.BroadcasterID)
	set("STREAMCHAT_KICK_WEBHOOK_URL", &c.Kick.WebhookURL)
	set("STREAMCHAT_KICK_REDIRECT_URI", &c.Kick.RedirectURI)
	set("STREAMCHAT_KICK_LISTEN", &c.Kick.Listen)
	set("STREAMCHAT_TWITCH_CLIENT_ID", &c.Twitch.ClientID)
	set("STREAMCHAT_TWITCH_CLIENT_SECRET", &c.Twitch.ClientSecret)
	set("STREAMCHAT_TWITCH_ACCESS_TOKEN", &c.Twitch.AccessToken)
	set("STREAMCHAT_TWITCH_REFRESH_TOKEN", &c.Twitch.RefreshToken)
	set("STREAMCHAT_TWITCH_USER_ID", &c.Twitch.UserID)
	set("STREAMCHAT_TWITCH_USER_LOGIN", &c.Twitch.UserLogin)
	set("STREAMCHAT_TWITCH_REDIRECT_URI", &c.Twitch.RedirectURI)
	set("STREAMCHAT_TWITCH_CHANNEL", &c.Twitch.Channel)
	set("STREAMCHAT_SERVER_LISTEN", &c.Server.Listen)
	set("STREAMCHAT_SERVER_WEBSOCKET_PATH", &c.Server.WebSocketPath)
	set("STREAMCHAT_CLIENT_SERVER_URL", &c.Client.ServerURL)
	set("STREAMCHAT_STORAGE_SQLITE_PATH", &c.Storage.SQLitePath)
	set("STREAMCHAT_EMOTES_MODE", &c.Emotes.Mode)
	set("STREAMCHAT_CHAT_COLOR_MODE", &c.ChatColorMode)
	if value := get("STREAMCHAT_EMOTES_DEBUG"); value != "" {
		if enabled, err := strconv.ParseBool(value); err == nil {
			c.Emotes.Debug = enabled
		}
	}
	set("STREAMCHAT_RELAY_AUTH_TOKEN", &c.RelayAuthToken)
	set("STREAMCHAT_LOG_FILE", &c.LogFile)
}

func (c Config) HasUsablePlatform() bool {
	return c.YouTube.APIKey != "" || c.YouTube.AccessToken != "" || (c.Kick.AccessToken != "" && c.Kick.WebhookURL != "") || (c.Twitch.ClientID != "" && (c.Twitch.AccessToken != "" || c.Twitch.RefreshToken != "")) || (c.Client.ServerURL != "" && c.RelayAuthToken != "")
}

func (c Config) Validate(mode string) error {
	if !chattercolor.ValidMode(c.ChatColorMode) {
		return errors.New("chat_color_mode must be line, username, or off")
	}
	if c.Emotes.Mode != "auto" && c.Emotes.Mode != "text" && c.Emotes.Mode != "off" {
		return errors.New("emotes.mode must be auto, text, or off")
	}
	if c.QueueSize < 1 || c.QueueSize > 65536 {
		return errors.New("queue_size must be between 1 and 65536")
	}
	if c.DuplicateCapacity < 1 || c.DuplicateCapacity > 1000000 {
		return errors.New("duplicate_capacity must be between 1 and 1000000")
	}
	if c.Bot.CooldownSeconds < 1 || c.Bot.CooldownSeconds > 3600 {
		return errors.New("bot.cooldown_seconds must be between 1 and 3600")
	}
	if strings.TrimSpace(c.Bot.CommandsReply) == "" {
		return errors.New("bot.commands_reply must not be blank")
	}
	if c.Bot.Discord.Enabled {
		if id, err := strconv.ParseUint(c.Bot.Discord.ChannelID, 10, 64); err != nil || id == 0 {
			return errors.New("bot.discord.channel_id must be a positive Discord channel ID")
		}
		if strings.TrimSpace(c.Bot.Discord.Message) == "" {
			return errors.New("bot.discord.message must not be blank")
		}
		if strings.TrimSpace(c.Bot.Discord.TokenEnv) == "" || strings.ContainsAny(c.Bot.Discord.TokenEnv, " \t\r\n=") {
			return errors.New("bot.discord.token_env must name one environment variable")
		}
		if c.Bot.Discord.Confirmations < 1 || c.Bot.Discord.Confirmations > 5 {
			return errors.New("bot.discord.confirmations must be between 1 and 5")
		}
	}
	if c.Bot.Subtitles.Enabled {
		s := c.Bot.Subtitles
		if strings.TrimSpace(c.Bot.GUIListen) == "" {
			return errors.New("bot.gui_listen is required when GPU subtitles are enabled")
		}
		if strings.TrimSpace(s.APIKeyEnv) == "" || strings.ContainsAny(s.APIKeyEnv, " \t\r\n=") {
			return errors.New("bot.subtitles.api_key_env must name one environment variable")
		}
		if u, err := url.Parse(s.APIBaseURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("bot.subtitles.api_base_url must be an absolute HTTPS URL")
		}
		if strings.TrimSpace(s.Image) == "" || len(s.GPUTypeIDs) == 0 {
			return errors.New("bot.subtitles.image and gpu_type_ids are required")
		}
		if s.CloudType != "SECURE" && s.CloudType != "COMMUNITY" {
			return errors.New("bot.subtitles.cloud_type must be SECURE or COMMUNITY")
		}
		if s.WorkerPort < 1 || s.WorkerPort > 65535 {
			return errors.New("bot.subtitles.worker_port must be a valid port")
		}
		if s.ReadyMinutes < 1 || s.ReadyMinutes > 60 || s.MaxMinutes < 1 || s.MaxMinutes > 1440 {
			return errors.New("bot.subtitles ready/max minutes are outside the safe range")
		}
		if s.HeartbeatSeconds < 30 || s.HeartbeatSeconds > 900 {
			return errors.New("bot.subtitles.heartbeat_seconds must be 30-900")
		}
		if s.MaxCostPerHour <= 0 || s.MaxCostPerHour > 5 {
			return errors.New("bot.subtitles.max_cost_per_hour must be greater than zero and at most 5")
		}
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
		if n, e := strconv.ParseInt(c.Kick.BroadcasterID, 10, 64); e != nil || n <= 0 {
			return errors.New("kick.broadcaster_id must be a positive integer")
		}
	}
	if c.Kick.WebhookURL != "" {
		u, e := url.Parse(c.Kick.WebhookURL)
		if e != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("kick.webhook_url must be an absolute HTTPS URL")
		}
		if u.Path != "/webhooks/kick" {
			return errors.New("kick.webhook_url path must be /webhooks/kick")
		}
	}
	serverHost, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return errors.New("server.listen must be a host:port address")
	}
	serverIP := net.ParseIP(serverHost)
	if serverHost != "localhost" && (serverIP == nil || (!serverIP.IsLoopback() && !serverIP.IsPrivate())) {
		return errors.New("server.listen must use localhost, a loopback IP, or a private IP")
	}
	if c.Server.WebSocketPath == "" || c.Server.WebSocketPath[0] != '/' || strings.ContainsAny(c.Server.WebSocketPath, "?#") {
		return errors.New("server.websocket_path must be an absolute path without a query or fragment")
	}
	if c.Server.WebSocketPath == "/" || c.Server.WebSocketPath == "/healthz" || c.Server.WebSocketPath == "/webhooks/kick" {
		return errors.New("server.websocket_path conflicts with a reserved endpoint")
	}
	if c.Client.ServerURL != "" {
		u, e := url.Parse(c.Client.ServerURL)
		if e != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("client.server_url must be an absolute ws:// or wss:// URL without credentials, query, or fragment")
		}
		if u.Path == "" || u.Path == "/" {
			return errors.New("client.server_url must include the WebSocket endpoint path")
		}
	}
	if c.RelayAuthToken != "" && (len(c.RelayAuthToken) < 32 || strings.TrimSpace(c.RelayAuthToken) != c.RelayAuthToken) {
		return errors.New("relay_auth_token must contain at least 32 non-whitespace characters")
	}
	if mode == "serve" && c.RelayAuthToken == "" {
		return errors.New("relay_auth_token is required for streamchat serve")
	}
	if mode == "serve" && strings.TrimSpace(c.Storage.SQLitePath) == "" {
		return errors.New("storage.sqlite_path is required for streamchat serve")
	}
	if mode == "serve" && (c.YouTube.ClientID != "" || c.YouTube.ClientSecret != "" || c.YouTube.RefreshToken != "") && (c.YouTube.ClientID == "" || c.YouTube.ClientSecret == "" || c.YouTube.RefreshToken == "") {
		return errors.New("server-side YouTube needs client_id, client_secret, and refresh_token; run: streamchat setup youtube-server")
	}
	if mode == "run" && c.Client.ServerURL != "" && c.RelayAuthToken == "" {
		return errors.New("relay_auth_token is required when client.server_url is configured")
	}
	if mode == "youtube" && (c.YouTube.VideoID == "" || (c.YouTube.APIKey == "" && c.YouTube.AccessToken == "")) {
		return errors.New("YouTube needs a live URL/video ID and a configured API key; run: streamchat setup youtube")
	}
	if mode == "twitch" && (c.Twitch.ClientID == "" || c.Twitch.AccessToken == "" || c.Twitch.Channel == "") {
		return errors.New("Twitch needs authorization and a channel; run: streamchat setup twitch")
	}
	return nil
}

func Redact(v string) string {
	if v == "" {
		return "<unset>"
	}
	return "<redacted>"
}

func Redacted(c Config) Config {
	r := c
	r.YouTube.APIKey = Redact(c.YouTube.APIKey)
	r.YouTube.ClientSecret = Redact(c.YouTube.ClientSecret)
	r.YouTube.AccessToken = Redact(c.YouTube.AccessToken)
	r.YouTube.RefreshToken = Redact(c.YouTube.RefreshToken)
	r.Kick.ClientSecret = Redact(c.Kick.ClientSecret)
	r.Kick.AccessToken = Redact(c.Kick.AccessToken)
	r.Kick.RefreshToken = Redact(c.Kick.RefreshToken)
	r.Twitch.ClientSecret = Redact(c.Twitch.ClientSecret)
	r.Twitch.AccessToken = Redact(c.Twitch.AccessToken)
	r.Twitch.RefreshToken = Redact(c.Twitch.RefreshToken)
	r.Bot.Kick.ClientSecret = Redact(c.Bot.Kick.ClientSecret)
	r.Bot.Kick.AccessToken = Redact(c.Bot.Kick.AccessToken)
	r.Bot.Kick.RefreshToken = Redact(c.Bot.Kick.RefreshToken)
	r.Bot.Twitch.ClientSecret = Redact(c.Bot.Twitch.ClientSecret)
	r.Bot.Twitch.AccessToken = Redact(c.Bot.Twitch.AccessToken)
	r.Bot.Twitch.RefreshToken = Redact(c.Bot.Twitch.RefreshToken)
	r.Bot.GUIPassword = Redact(c.Bot.GUIPassword)
	r.RelayAuthToken = Redact(c.RelayAuthToken)
	return r
}

func RedactedJSON(c Config) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(Redacted(c)); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func CheckFileMode(path string) error {
	if path == "" {
		path = DefaultPath()
	}
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config file permissions are %04o; secrets require 0600 (run: chmod 600 %s)", st.Mode().Perm(), path)
	}
	return nil
}
