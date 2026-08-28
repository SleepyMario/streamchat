package clientruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/archive"
	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientstate"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/outbound"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
	"github.com/SleepyMario/streamchat/internal/setup"
	"github.com/SleepyMario/streamchat/internal/terminalui"
)

type StreamStatus struct {
	Platform    string `json:"platform"`
	Title       string `json:"title,omitempty"`
	Category    string `json:"category,omitempty"`
	ViewerCount int    `json:"viewer_count,omitempty"`
	Live        bool   `json:"live"`
	Available   bool   `json:"available"`
	Error       string `json:"error,omitempty"`
}

type State struct {
	Selected string                  `json:"selected"`
	Relay    string                  `json:"relay"`
	Targets  map[string]bool         `json:"targets"`
	Streams  map[string]StreamStatus `json:"streams"`
}

type Health struct {
	Path       string            `json:"path"`
	Valid      bool              `json:"valid"`
	FileModeOK bool              `json:"file_mode_ok"`
	Platforms  map[string]string `json:"platforms"`
	Relay      string            `json:"relay"`
	Archive    string            `json:"archive"`
	Problems   []string          `json:"problems,omitempty"`
	Config     config.Config     `json:"config"`
}

type SetupGuide struct {
	Platform string   `json:"platform"`
	Status   string   `json:"status"`
	Command  string   `json:"command"`
	Steps    []string `json:"steps"`
}

type Runtime struct {
	cfg       config.Config
	path      string
	http      *http.Client
	session   *outbound.Session
	kick      *kickTarget
	twitch    *twitchTarget
	twitchErr string
	mu        sync.RWMutex
	relay     string
	streams   map[string]StreamStatus
	listeners []func(State)
}

func New(ctx context.Context, cfg config.Config) (*Runtime, error) {
	r := &Runtime{cfg: cfg, path: cfg.Path, http: &http.Client{Timeout: 20 * time.Second}, relay: "disconnected", streams: map[string]StreamStatus{}}
	r.kick = &kickTarget{cfg: cfg.Kick, path: cfg.Path, http: r.http}
	twitchTarget, twitchErr := newTwitchTarget(ctx, &r.cfg, r.path)
	r.twitch = twitchTarget
	if twitchErr != nil {
		r.twitchErr = twitchErr.Error()
	}
	kickAvailable := cfg.Kick.BroadcasterID != "" && (cfg.Kick.AccessToken != "" || cfg.Kick.RefreshToken != "")
	twitchAvailable := twitchErr == nil && twitchTarget != nil
	r.session = outbound.NewTargets(
		outbound.Target{Name: "kick", Aliases: []string{"kick"}, Sender: r.kick, Unavailable: !kickAvailable},
		outbound.Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: r.twitch, Unavailable: !twitchAvailable},
	)
	r.session.RegisterTargetControl("title", "kick", outbound.ControlFunc(r.kick.title))
	r.session.RegisterTargetControl("category", "kick", outbound.ControlFunc(r.kick.category))
	if r.twitch != nil {
		r.session.RegisterTargetControl("title", "twitch", outbound.ControlFunc(r.twitch.title))
		r.session.RegisterTargetControl("category", "twitch", outbound.ControlFunc(r.twitch.category))
	}
	r.session.RegisterControl("ban", outbound.ControlFunc(r.ban))
	r.session.RegisterControl("timeout", outbound.ControlFunc(r.timeout))
	r.session.RegisterControl("clear", outbound.ControlFunc(r.clear))
	if state, err := clientstate.Default(); err == nil {
		r.session.Restore(state.Load().LastOutboundTarget)
		r.session.SetSelectionChanged(func(target string) {
			_ = state.Save(clientstate.State{LastOutboundTarget: target})
			r.notify()
		})
	} else {
		r.session.SetSelectionChanged(func(string) { r.notify() })
	}
	return r, nil
}

func (r *Runtime) Session() *outbound.Session { return r.session }

func (r *Runtime) Execute(ctx context.Context, command string) (string, error) {
	if r == nil || r.session == nil {
		return "", errors.New("Streamchat runtime is unavailable")
	}
	return r.session.Process(ctx, command)
}

func (r *Runtime) Select(ctx context.Context, platform string) error {
	_, err := r.Execute(ctx, "/"+strings.ToLower(strings.TrimSpace(platform)))
	return err
}

func (r *Runtime) SetRelayState(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "disconnected"
	}
	r.mu.Lock()
	changed := r.relay != value
	r.relay = value
	r.mu.Unlock()
	if changed {
		r.notify()
	}
}

func (r *Runtime) Subscribe(callback func(State)) {
	if callback == nil {
		return
	}
	r.mu.Lock()
	r.listeners = append(r.listeners, callback)
	r.mu.Unlock()
	callback(r.State())
}

func (r *Runtime) notify() {
	state := r.State()
	r.mu.RLock()
	listeners := append([]func(State){}, r.listeners...)
	r.mu.RUnlock()
	for _, listener := range listeners {
		listener(state)
	}
}

func (r *Runtime) State() State {
	r.mu.RLock()
	relayState := r.relay
	streams := make(map[string]StreamStatus, len(r.streams))
	for name, status := range r.streams {
		streams[name] = status
	}
	r.mu.RUnlock()
	if r.twitchErr != "" {
		streams["twitch"] = StreamStatus{Platform: "twitch", Error: r.twitchErr}
	}
	return State{
		Selected: r.session.Selected(),
		Relay:    relayState,
		Targets: map[string]bool{
			"kick":    r.cfg.Kick.BroadcasterID != "" && (r.cfg.Kick.AccessToken != "" || r.cfg.Kick.RefreshToken != ""),
			"twitch":  r.twitch != nil,
			"youtube": r.cfg.YouTube.APIKey != "" || r.cfg.YouTube.RefreshToken != "" || r.cfg.Client.ServerURL != "",
		},
		Streams: streams,
	}
}

func (r *Runtime) RefreshStatus(ctx context.Context) State {
	state := r.State()
	if state.Targets["kick"] {
		state.Streams["kick"] = r.kick.status(ctx)
	}
	if state.Targets["twitch"] {
		state.Streams["twitch"] = r.twitch.status(ctx)
	}
	r.mu.Lock()
	r.streams = state.Streams
	r.mu.Unlock()
	r.notify()
	return state
}

// StreamStatus makes Runtime the status provider used by the terminal frontend.
// The selected provider is the same selection used by GUI and CLI sending.
func (r *Runtime) StreamStatus(ctx context.Context) (terminalui.StreamStatus, error) {
	selected := r.session.Selected()
	switch selected {
	case "kick":
		status, err := r.kick.rawStatus(ctx)
		return terminalui.StreamStatus{Title: status.Title, Category: status.Category, ViewerCount: status.ViewerCount, Live: status.Live}, err
	case "twitch":
		if r.twitch == nil {
			return terminalui.StreamStatus{}, errors.New("Twitch is unavailable")
		}
		status := r.twitch.status(ctx)
		if status.Error != "" {
			return terminalui.StreamStatus{}, errors.New(status.Error)
		}
		return terminalui.StreamStatus{Title: status.Title, Category: status.Category, ViewerCount: status.ViewerCount, Live: status.Live}, nil
	default:
		return terminalui.StreamStatus{}, errors.New("select Kick or Twitch")
	}
}

func (r *Runtime) ConfigHealth() Health {
	return InspectConfig(r.cfg)
}

func InspectConfig(cfg config.Config) Health {
	h := Health{Path: cfg.Path, Platforms: map[string]string{}, Config: config.Redacted(cfg), Relay: "not configured", Archive: cfg.Storage.SQLitePath}
	if err := cfg.Validate("check"); err != nil {
		h.Problems = append(h.Problems, err.Error())
	} else {
		h.Valid = true
	}
	if err := config.CheckFileMode(cfg.Path); err != nil {
		h.Problems = append(h.Problems, err.Error())
	} else {
		h.FileModeOK = true
	}
	if cfg.Client.ServerURL != "" && cfg.RelayAuthToken != "" {
		h.Relay = "configured"
	}
	if cfg.Kick.BroadcasterID != "" && (cfg.Kick.AccessToken != "" || cfg.Kick.RefreshToken != "") {
		h.Platforms["kick"] = "configured"
	} else {
		h.Platforms["kick"] = "setup required"
	}
	if cfg.Twitch.Channel != "" && cfg.Twitch.ClientID != "" && (cfg.Twitch.AccessToken != "" || cfg.Twitch.RefreshToken != "") {
		h.Platforms["twitch"] = "configured"
	} else {
		h.Platforms["twitch"] = "setup required"
	}
	if cfg.YouTube.APIKey != "" || cfg.YouTube.RefreshToken != "" {
		h.Platforms["youtube"] = "configured"
	} else {
		h.Platforms["youtube"] = "setup required"
	}
	return h
}

func RedactedConfigJSON(cfg config.Config) ([]byte, error) { return config.RedactedJSON(cfg) }

func (r *Runtime) SetupGuides() []SetupGuide {
	health := r.ConfigHealth()
	return []SetupGuide{
		{Platform: "kick", Status: health.Platforms["kick"], Command: "streamchat setup kick", Steps: []string{"Create or open the Kick developer application.", "Confirm its OAuth redirect and public webhook URLs.", "Run the setup command and complete authorization in the browser.", "Run streamchat kick subscribe after changing the portal webhook URL."}},
		{Platform: "twitch", Status: health.Platforms["twitch"], Command: "streamchat setup twitch", Steps: []string{"Create or open the Twitch developer application.", "Confirm the OAuth redirect URL.", "Run the setup command and authorize chat, channel-management, and moderation scopes."}},
		{Platform: "youtube", Status: health.Platforms["youtube"], Command: "streamchat setup youtube", Steps: []string{"Enable YouTube Data API v3 in Google Cloud.", "Create an API key for public chat, or use streamchat setup youtube-server for active-broadcast OAuth.", "Complete the browser authorization when prompted."}},
	}
}

func (r *Runtime) ArchiveStats(ctx context.Context) (archive.Stats, error) {
	return ReadArchiveStats(ctx, r.cfg)
}

func ReadArchiveStats(ctx context.Context, cfg config.Config) (archive.Stats, error) {
	if _, err := os.Stat(cfg.Storage.SQLitePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return archive.Stats{}, errors.New("Streamchat archive does not exist yet")
		}
		return archive.Stats{}, err
	}
	store, err := archive.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return archive.Stats{}, err
	}
	defer store.Close()
	return store.Stats(ctx)
}

func RunSetup(ctx context.Context, in io.Reader, out io.Writer, path string, selected []string) error {
	return setup.New(in, out, path).Run(ctx, selected)
}

func (r *Runtime) OpenURL(ctx context.Context, platform string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "kick":
		status, err := r.kick.rawStatus(ctx)
		if err != nil {
			return "", err
		}
		slug := strings.TrimSpace(status.Slug)
		if slug == "" || strings.ContainsAny(slug, "/?#") {
			return "", errors.New("Kick channel URL is unavailable")
		}
		return "https://kick.com/" + url.PathEscape(slug), nil
	case "twitch":
		channel, err := twitch.ParseChannel(r.cfg.Twitch.Channel)
		if err != nil {
			return "", errors.New("Twitch channel is unavailable")
		}
		return "https://www.twitch.tv/" + url.PathEscape(channel), nil
	case "youtube":
		videoID, err := youtube.ParseVideoID(r.cfg.YouTube.VideoID)
		if err != nil {
			return "", errors.New("YouTube stream is unavailable")
		}
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
	default:
		return "", errors.New("unsupported platform")
	}
}

func (r *Runtime) ban(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 2 {
		return "", errors.New("select a platform and enter a username")
	}
	switch strings.ToLower(fields[0]) {
	case "kick":
		return r.kick.ban(ctx, fields[1])
	case "twitch":
		if r.twitch == nil {
			return "", errors.New("Twitch is unavailable")
		}
		return r.twitch.ban(ctx, fields[1])
	default:
		return "", errors.New("unsupported moderation platform")
	}
}

func (r *Runtime) timeout(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) != 3 {
		return "", errors.New("select a platform, username, and duration")
	}
	switch strings.ToLower(fields[0]) {
	case "kick":
		return r.kick.timeout(ctx, fields[1], fields[2])
	case "twitch":
		if r.twitch == nil {
			return "", errors.New("Twitch is unavailable")
		}
		return r.twitch.timeout(ctx, fields[1], fields[2])
	default:
		return "", errors.New("unsupported moderation platform")
	}
}

func (r *Runtime) clear(ctx context.Context, argument string) (string, error) {
	fields := strings.Fields(argument)
	if len(fields) == 0 || len(fields) > 2 {
		return "", errors.New("select kick or twitch for remote clear")
	}
	platform := strings.ToLower(fields[0])
	if platform == "twitch" && len(fields) == 1 {
		if r.twitch == nil {
			return "", errors.New("Twitch is unavailable")
		}
		if err := r.twitch.clear(ctx); err != nil {
			return "", err
		}
		return "Twitch chat cleared.", nil
	}
	if platform != "kick" {
		return "", errors.New("unsupported remote-clear platform")
	}
	if platform != "kick" && platform != "twitch" {
		return "", errors.New("unsupported remote-clear platform")
	}
	days := 1
	if len(fields) > 1 {
		value := strings.TrimSuffix(strings.ToLower(fields[1]), "d")
		if _, err := fmt.Sscanf(value, "%d", &days); err != nil || days < 1 || days > 3650 {
			return "", errors.New("clear window must be 1 to 3650 days")
		}
	}
	store, err := archive.Open(r.cfg.Storage.SQLitePath)
	if err != nil {
		return "", err
	}
	defer store.Close()
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	if platform == "twitch" {
		if r.twitch == nil {
			return "", errors.New("Twitch is unavailable")
		}
		references, err := store.MessageReferencesSince(ctx, chat.PlatformTwitch, since)
		if err != nil {
			return "", err
		}
		cutoff := time.Now().UTC().Add(-6 * time.Hour)
		ids := make([]string, 0, len(references))
		skipped := 0
		for _, reference := range references {
			if !reference.Timestamp.UTC().After(cutoff) {
				skipped++
				continue
			}
			ids = append(ids, reference.ID)
		}
		deleted, failed, err := r.twitch.clearMessages(ctx, ids)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Twitch: deleted %d known messages; %d failed; %d older than the 6-hour individual-delete limit.", deleted, failed, skipped), nil
	}
	ids, err := store.MessageIDsSince(ctx, chat.PlatformKick, since)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return fmt.Sprintf("No archived Kick messages to clear in the last %dd.", days), nil
	}
	deleted, failed, err := r.kick.clear(ctx, ids)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Kick remote clear finished: %d deleted, %d failed.", deleted, failed), nil
}

type kickTarget struct {
	mu   sync.Mutex
	cfg  config.Kick
	path string
	http *http.Client
}

func (k *kickTarget) Send(ctx context.Context, message string) error {
	_, err := k.SendMessage(ctx, message)
	return err
}

func (k *kickTarget) SendMessage(ctx context.Context, message string) (outbound.SentMessage, error) {
	var receipt kick.ChatReceipt
	err := k.withToken(ctx, func(token string) error {
		var sendErr error
		receipt, sendErr = (kick.ChatClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}).SendMessage(ctx, k.cfg.BroadcasterID, message)
		return sendErr
	})
	return outbound.SentMessage{ID: receipt.MessageID, AuthorID: k.cfg.BroadcasterID, AuthorDisplayName: "you"}, err
}

func (k *kickTarget) title(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("title must not be empty")
	}
	err := k.withToken(ctx, func(token string) error {
		return (kick.ChannelClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}).UpdateTitle(ctx, value)
	})
	if err != nil {
		return "", err
	}
	return "Title updated: " + value, nil
}

func (k *kickTarget) category(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("category must not be empty")
	}
	var category kick.Category
	err := k.withToken(ctx, func(token string) error {
		client := kick.ChannelClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}
		var err error
		category, err = client.ResolveCategory(ctx, value)
		if err != nil {
			return err
		}
		return client.UpdateCategory(ctx, category.ID)
	})
	if err != nil {
		return "", err
	}
	return "Category updated: " + category.Name, nil
}

func (k *kickTarget) rawStatus(ctx context.Context) (kick.ChannelStatus, error) {
	var s kick.ChannelStatus
	err := k.withToken(ctx, func(token string) error {
		var e error
		s, e = (kick.ChannelClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}).GetStatus(ctx)
		return e
	})
	return s, err
}
func (k *kickTarget) status(ctx context.Context) StreamStatus {
	s, err := k.rawStatus(ctx)
	out := StreamStatus{Platform: "kick", Title: s.Title, Category: s.Category, ViewerCount: s.ViewerCount, Live: s.Live, Available: err == nil}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}
func (k *kickTarget) ban(ctx context.Context, user string) (string, error) {
	var u kick.ModerationUser
	err := k.withToken(ctx, func(token string) error {
		var e error
		u, e = (kick.ModerationClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}).Ban(ctx, k.cfg.BroadcasterID, user)
		return e
	})
	if err != nil {
		return "", err
	}
	return "Banned: " + u.Username, nil
}
func (k *kickTarget) timeout(ctx context.Context, user, duration string) (string, error) {
	minutes, err := kick.ParseTimeoutDuration(duration)
	if err != nil {
		return "", err
	}
	var u kick.ModerationUser
	err = k.withToken(ctx, func(token string) error {
		u, err = (kick.ModerationClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}).Timeout(ctx, k.cfg.BroadcasterID, user, minutes)
		return err
	})
	if err != nil {
		return "", err
	}
	return "Timed out: " + u.Username + " for " + duration, nil
}
func (k *kickTarget) clear(ctx context.Context, ids []string) (int, int, error) {
	deleted, failed := 0, 0
	err := k.withToken(ctx, func(token string) error {
		client := kick.ChatDeleteClient{HTTP: k.http, BaseURL: k.cfg.APIBaseURL, AccessToken: token}
		for _, id := range ids {
			if err := client.DeleteMessage(ctx, id); err != nil {
				failed++
			} else {
				deleted++
			}
		}
		return nil
	})
	return deleted, failed, err
}

func (k *kickTarget) withToken(ctx context.Context, operation func(string) error) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cfg.AccessToken == "" || (!k.cfg.TokenExpiry.IsZero() && time.Until(k.cfg.TokenExpiry) < time.Minute) {
		if k.cfg.RefreshToken == "" {
			return errors.New("Kick authorization is missing; run streamchat setup kick")
		}
		if err := k.refresh(ctx); err != nil {
			return err
		}
	}
	err := operation(k.cfg.AccessToken)
	if kickAuthenticationError(err) && k.cfg.RefreshToken != "" {
		if refreshErr := k.refresh(ctx); refreshErr == nil {
			return operation(k.cfg.AccessToken)
		}
	}
	return err
}

func kickAuthenticationError(err error) bool {
	return errors.Is(err, kick.ErrChatAuthentication) || errors.Is(err, kick.ErrChannelAuthentication) || errors.Is(err, kick.ErrModerationAuthentication) || errors.Is(err, kick.ErrChatDeleteAuthentication)
}
func (k *kickTarget) refresh(ctx context.Context) error {
	oauth := kick.OAuthClient{HTTP: k.http, OAuthBaseURL: k.cfg.OAuthBaseURL, APIBaseURL: k.cfg.APIBaseURL, ClientID: k.cfg.ClientID, ClientSecret: k.cfg.ClientSecret}
	tok, err := oauth.Refresh(ctx, k.cfg.RefreshToken)
	if err != nil {
		return err
	}
	k.cfg.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		k.cfg.RefreshToken = tok.RefreshToken
	}
	k.cfg.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if os.Getenv("STREAMCHAT_KICK_ACCESS_TOKEN") != "" {
		return nil
	}
	loaded, err := config.Load(k.path)
	if err != nil {
		return err
	}
	loaded.Kick.AccessToken = k.cfg.AccessToken
	loaded.Kick.RefreshToken = k.cfg.RefreshToken
	loaded.Kick.TokenExpiry = k.cfg.TokenExpiry
	return config.Save(k.path, loaded)
}

type twitchTarget struct {
	chat              *twitch.ChatSender
	channel           *twitch.ChannelClient
	moderation        *twitch.ModerationClient
	userID, userLogin string
}

func newTwitchTarget(ctx context.Context, c *config.Config, path string) (*twitchTarget, error) {
	if c.Twitch.ClientID == "" || c.Twitch.Channel == "" || (c.Twitch.AccessToken == "" && c.Twitch.RefreshToken == "") {
		return nil, errors.New("Twitch is not configured")
	}
	api := &twitch.API{HTTP: &http.Client{Timeout: 20 * time.Second}, APIBaseURL: c.Twitch.APIBaseURL, OAuthBaseURL: c.Twitch.OAuthBaseURL, ClientID: c.Twitch.ClientID, ClientSecret: c.Twitch.ClientSecret, AccessToken: c.Twitch.AccessToken, RefreshToken: c.Twitch.RefreshToken}
	api.OnToken = func(tok twitch.Token) error {
		c.Twitch.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			c.Twitch.RefreshToken = tok.RefreshToken
		}
		c.Twitch.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		if os.Getenv("STREAMCHAT_TWITCH_ACCESS_TOKEN") != "" {
			return nil
		}
		loaded, err := config.Load(path)
		if err != nil {
			return err
		}
		loaded.Twitch = c.Twitch
		return config.Save(path, loaded)
	}
	identity, err := api.ValidateToken(ctx)
	if err != nil && c.Twitch.RefreshToken != "" {
		tok, e := api.Refresh(ctx, c.Twitch.RefreshToken)
		if e != nil {
			return nil, e
		}
		api.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			api.RefreshToken = tok.RefreshToken
		}
		if api.OnToken != nil {
			if e = api.OnToken(tok); e != nil {
				return nil, e
			}
		}
		identity, err = api.ValidateToken(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err = twitch.RequireScopes(identity.Scopes, twitch.RequiredChatScopes...); err != nil {
		return nil, err
	}
	channel, err := api.User(ctx, c.Twitch.Channel)
	if err != nil {
		return nil, err
	}
	c.Twitch.UserID = identity.UserID
	c.Twitch.UserLogin = identity.Login
	auth := twitch.NewUserClient(api, identity.Scopes)
	return &twitchTarget{chat: twitch.NewChatSenderWithUserClient(auth, channel.ID, identity.UserID), channel: twitch.NewChannelClient(auth, channel.ID, identity.UserID), moderation: twitch.NewModerationClient(auth, channel.ID, identity.UserID), userID: identity.UserID, userLogin: identity.Login}, nil
}
func (t *twitchTarget) Send(ctx context.Context, message string) error {
	_, err := t.SendMessage(ctx, message)
	return err
}
func (t *twitchTarget) SendMessage(ctx context.Context, message string) (outbound.SentMessage, error) {
	id, err := t.chat.SendMessage(ctx, message)
	return outbound.SentMessage{ID: id, AuthorID: t.userID, AuthorDisplayName: t.userLogin}, err
}
func (t *twitchTarget) title(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("title must not be empty")
	}
	if err := t.channel.UpdateTitle(ctx, value); err != nil {
		return "", err
	}
	return "Title updated: " + value, nil
}
func (t *twitchTarget) category(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("category must not be empty")
	}
	if err := t.channel.RequireManagement(); err != nil {
		return "", err
	}
	category, err := t.channel.ResolveCategory(ctx, value)
	if err != nil {
		return "", err
	}
	if err = t.channel.UpdateCategory(ctx, category.ID); err != nil {
		return "", err
	}
	return "Category updated: " + category.Name, nil
}
func (t *twitchTarget) status(ctx context.Context) StreamStatus {
	s, err := t.channel.GetStatus(ctx)
	out := StreamStatus{Platform: "twitch", Title: s.Title, Category: s.Category, ViewerCount: s.ViewerCount, Live: s.Live, Available: err == nil}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}
func (t *twitchTarget) ban(ctx context.Context, user string) (string, error) {
	u, err := t.moderation.Ban(ctx, user)
	if err != nil {
		return "", err
	}
	name := u.DisplayName
	if name == "" {
		name = u.Login
	}
	return "Banned: " + name, nil
}
func (t *twitchTarget) timeout(ctx context.Context, user, duration string) (string, error) {
	seconds, err := twitch.ParseTimeoutDuration(duration)
	if err != nil {
		return "", err
	}
	u, err := t.moderation.Timeout(ctx, user, seconds)
	if err != nil {
		return "", err
	}
	name := u.DisplayName
	if name == "" {
		name = u.Login
	}
	return "Timed out: " + name + " for " + duration, nil
}
func (t *twitchTarget) clear(ctx context.Context) error { return t.moderation.ClearChat(ctx) }
func (t *twitchTarget) clearMessages(ctx context.Context, ids []string) (int, int, error) {
	deleted, failed := 0, 0
	for _, id := range ids {
		if err := t.moderation.DeleteMessage(ctx, id); err != nil {
			failed++
		} else {
			deleted++
		}
	}
	return deleted, failed, nil
}
