package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SleepyMario/streamchat/internal/config"
	oauthpkg "github.com/SleepyMario/streamchat/internal/oauth"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/SleepyMario/streamchat/internal/platform/youtube"
	"golang.org/x/term"
)

// The alias is intentionally local: it keeps the wizard prose readable while
// making the OAuth implementation injectable in tests.
type AuthorizeFunc func(context.Context, oauthpkg.Request, io.Writer, func(string) error) (oauthpkg.Result, error)

var ErrCancelled = errors.New("setup cancelled")

var youtubeServerScopes = []string{youtube.ForceSSLScope}

type Wizard struct {
	In          *bufio.Reader
	rawIn       io.Reader
	Out         io.Writer
	Path        string
	HTTP        *http.Client
	OpenBrowser func(string) error
	Authorize   AuthorizeFunc
}

func New(in io.Reader, out io.Writer, path string) *Wizard {
	return &Wizard{In: bufio.NewReader(in), rawIn: in, Out: out, Path: path, HTTP: &http.Client{Timeout: 20 * time.Second}, Authorize: oauthpkg.Authorize}
}

func ParseSelection(s string) ([]string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil, ErrCancelled
	}
	if s == "all" || s == "1,2,3" {
		return []string{"youtube", "kick", "twitch"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		var n string
		switch p {
		case "1", "youtube":
			n = "youtube"
		case "2", "kick":
			n = "kick"
		case "3", "twitch":
			n = "twitch"
		default:
			return nil, fmt.Errorf("unknown selection %q; enter 1, 2, 3, or a comma-separated list", p)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, ErrCancelled
	}
	return out, nil
}

func (w *Wizard) line(prompt string) (string, error) {
	fmt.Fprint(w.Out, prompt)
	s, e := w.In.ReadString('\n')
	if e != nil && len(s) == 0 {
		if errors.Is(e, io.EOF) {
			return "", ErrCancelled
		}
		return "", e
	}
	return strings.TrimSpace(s), nil
}

func (w *Wizard) value(label, current string, secret bool) (string, error) {
	if current != "" {
		ans, e := w.line(fmt.Sprintf("%s is already configured. Keep it? [Y/n] ", label))
		if e != nil {
			return "", e
		}
		if ans == "" || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			return current, nil
		}
	}
	var v string
	var e error
	if f, ok := w.rawIn.(*os.File); secret && ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(w.Out, label+": ")
		var b []byte
		b, e = term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(w.Out)
		v = strings.TrimSpace(string(b))
	} else {
		v, e = w.line(label + ": ")
	}
	if e != nil {
		return "", e
	}
	if v == "" {
		return "", fmt.Errorf("%s cannot be empty", label)
	}
	if secret {
		fmt.Fprintln(w.Out, "Saved securely (value not displayed).")
	}
	return v, nil
}

func (w *Wizard) Run(ctx context.Context, only []string) error {
	c, err := config.Load(w.Path)
	if err != nil {
		return err
	}
	selected := only
	if len(selected) == 0 {
		if c.HasUsablePlatform() {
			fmt.Fprintln(w.Out, "Which services do you want to configure?")
		} else {
			fmt.Fprintln(w.Out, "Streamchat is not configured yet.\n\nWhich services do you want to use?")
		}
		fmt.Fprintln(w.Out, "\n  [1] YouTube\n  [2] Kick\n  [3] Twitch\n\nSelect one or more (for example 1,3 or all). Press Ctrl-C or Enter to cancel.")
		s, err := w.line("Selection: ")
		if err != nil {
			return err
		}
		selected, err = ParseSelection(s)
		if err != nil {
			return err
		}
	}
	for _, name := range selected {
		switch name {
		case "youtube":
			err = w.youtube(ctx, &c)
		case "youtube-server":
			err = w.youtubeServer(ctx, &c)
		case "kick":
			err = w.kick(ctx, &c)
		case "twitch":
			err = w.twitch(ctx, &c)
		case "twitch-bot":
			err = w.twitchBot(ctx, &c)
		default:
			err = fmt.Errorf("unknown platform %q", name)
		}
		if err != nil {
			return err
		}
		if err = config.Save(w.Path, c); err != nil {
			return err
		}
		display := strings.ToUpper(name[:1]) + name[1:]
		if name == "youtube-server" {
			display = "YouTube server"
		} else if name == "twitch-bot" {
			display = "Twitch bot"
		}
		fmt.Fprintf(w.Out, "%s configuration saved to %s.\n", display, pathOrDefault(w.Path))
	}
	return nil
}

func (w *Wizard) youtubeServer(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, `Server-side YouTube integration uses Google's official OAuth 2.0 desktop-app flow and the youtube.force-ssl scope.

1. Enable YouTube Data API v3 in a Google Cloud project.
2. Configure the OAuth consent screen for that project.
3. Create an OAuth Client ID whose application type is Desktop app.
4. Streamchat opens a loopback callback on `+c.YouTube.RedirectURI+`.

Offline access stores a refresh token so streamchat serve can discover the authenticated account's active broadcast and ingest chat while no interactive client is running. This authorization also permits Streamchat's YouTube chat sending and moderation features as they are connected to the shared runtime.`)
	var err error
	c.YouTube.ClientID, err = w.value("Google OAuth Client ID", c.YouTube.ClientID, false)
	if err != nil {
		return err
	}
	c.YouTube.ClientSecret, err = w.value("Google OAuth Client Secret", c.YouTube.ClientSecret, true)
	if err != nil {
		return err
	}
	res, err := w.Authorize(ctx, oauthpkg.Request{
		AuthorizeURL: youtube.AuthorizeURL,
		ClientID:     c.YouTube.ClientID,
		RedirectURI:  c.YouTube.RedirectURI,
		Scopes:       youtubeServerScopes,
		UsePKCE:      true,
		Parameters: map[string]string{
			"access_type":            "offline",
			"include_granted_scopes": "true",
			"prompt":                 "consent",
		},
	}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	oc := youtube.OAuthClient{HTTP: w.HTTP, ClientID: c.YouTube.ClientID, ClientSecret: c.YouTube.ClientSecret}
	tok, err := oc.Exchange(ctx, res.Code, c.YouTube.RedirectURI, res.Verifier)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("Google did not return a refresh token; revoke the prior grant if necessary, then run: streamchat setup youtube-server")
	}
	c.YouTube.AccessToken = tok.AccessToken
	c.YouTube.RefreshToken = tok.RefreshToken
	c.YouTube.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	fmt.Fprintln(w.Out, "YouTube server authorization saved. streamchat serve will discover active broadcasts automatically.")
	return nil
}

func pathOrDefault(p string) string {
	if p == "" {
		return config.DefaultPath()
	}
	return p
}

func (w *Wizard) youtube(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, `YouTube public live chat uses a YouTube Data API v3 API key.

An API key identifies your Google Cloud project so YouTube can account for quota. It is not an OAuth user token and does not grant access to private account data.

1. Create or select a project: https://console.cloud.google.com/projectcreate
2. Enable YouTube Data API v3: https://console.cloud.google.com/apis/library/youtube.googleapis.com
3. Create an API key: https://console.cloud.google.com/apis/credentials
4. In the key settings, restrict the API to YouTube Data API v3. Application restrictions for an installed CLI depend on your environment; do not use HTTP-referrer restrictions, which are for websites.

Streamchat requests public videos.list and liveChatMessages.list data. No OAuth scope is requested for this read-only public-chat path.`)
	old := c.YouTube.APIKey
	key, err := w.value("YouTube API key", old, true)
	if err != nil {
		return err
	}
	c.YouTube.APIKey = key
	if key != old {
		cl := youtube.New(w.HTTP, c.YouTube.BaseURL, key, "", "")
		if err = cl.ValidateCredential(ctx); err != nil {
			return fmt.Errorf("YouTube API key validation failed: %w\nCheck that YouTube Data API v3 is enabled, then run: streamchat setup youtube", err)
		}
		fmt.Fprintln(w.Out, "YouTube API key validated with one low-quota videos.list request.")
	}
	return nil
}

var kickOAuthScopes = []string{"user:read", "events:subscribe", kick.ChatWriteScope, kick.ChannelWriteScope, kick.ChannelReadScope, kick.ModerationBanScope, kick.ChatMessageManageScope}

func (w *Wizard) kick(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, kickInstructions(*c))
	var err error
	c.Kick.ClientID = strings.TrimSpace(c.Kick.ClientID)
	c.Kick.ClientSecret = strings.TrimSpace(c.Kick.ClientSecret)
	c.Kick.ClientID, err = w.value("Kick Client ID", c.Kick.ClientID, false)
	if err != nil {
		return err
	}
	c.Kick.ClientSecret, err = w.value("Kick Client Secret", c.Kick.ClientSecret, true)
	if err != nil {
		return err
	}
	c.Kick.WebhookURL, err = w.value("Webhook URL configured in the Kick developer portal (must end in /webhooks/kick)", c.Kick.WebhookURL, false)
	if err != nil {
		return err
	}
	if e := c.Validate("check"); e != nil {
		return e
	}
	res, err := w.Authorize(ctx, oauthpkg.Request{AuthorizeURL: strings.TrimRight(c.Kick.OAuthBaseURL, "/") + "/oauth/authorize", ClientID: c.Kick.ClientID, RedirectURI: c.Kick.RedirectURI, Scopes: kickOAuthScopes, UsePKCE: true}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	oc := kick.OAuthClient{HTTP: w.HTTP, OAuthBaseURL: c.Kick.OAuthBaseURL, APIBaseURL: c.Kick.APIBaseURL, ClientID: c.Kick.ClientID, ClientSecret: c.Kick.ClientSecret}
	tok, err := oc.Exchange(ctx, res.Code, c.Kick.RedirectURI, res.Verifier)
	if err != nil {
		return err
	}
	id, name, err := oc.CurrentUser(ctx, tok.AccessToken)
	if err != nil {
		return err
	}
	c.Kick.AccessToken = tok.AccessToken
	c.Kick.RefreshToken = tok.RefreshToken
	c.Kick.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	c.Kick.BroadcasterID = id
	fmt.Fprintf(w.Out, "Authorized as Kick user %s (broadcaster user ID %s). Streamchat resolved this ID automatically.\n", name, id)
	cl := kick.SubscriptionClient{HTTP: w.HTTP, BaseURL: c.Kick.APIBaseURL, AccessToken: c.Kick.AccessToken}
	if _, err = cl.Do(ctx, http.MethodPost, id); err != nil {
		return err
	}
	fmt.Fprintln(w.Out, "Kick event subscription created. Delivery uses the webhook URL in the Kick developer portal; the local kick.webhook_url value was not sent.")
	return nil
}

func kickInstructions(c config.Config) string {
	return `Kick delivers official chat.message.sent events to a verified webhook.

1. Sign in to Kick, enable 2FA, and create an app at https://kick.com/settings/developer
2. A Client ID identifies the app. A Client Secret authenticates it and must remain private.
3. Register this exact OAuth redirect URI in the app settings:
   ` + c.Kick.RedirectURI + `
4. In the Kick developer portal, configure a publicly reachable HTTPS webhook
   whose path is /webhooks/kick.
   Streamchat listens locally on 127.0.0.1:8788 and handles /webhooks/kick.
   Kick cannot reach 127.0.0.1; use a trusted HTTPS tunnel or reverse proxy so
   https://your-host.example/webhooks/kick maps to http://127.0.0.1:8788/webhooks/kick.
5. Enter that same portal webhook URL below as kick.webhook_url.
   This local value is for validation and operator reference.
   Streamchat does not send it in the Kick event-subscription request.
   Changing the JSON value alone does not change Kick's webhook destination.

After changing the webhook URL in the Kick developer portal, run:
  streamchat kick subscribe

Streamchat requests user:read (to obtain your broadcaster user ID), events:subscribe (to create the chat webhook subscription), chat:write (to send messages), channel:write (to update your Kick stream title and category), channel:read (to fetch channel status and resolve moderation targets by channel slug), moderation:ban (to ban or timeout users), and moderation:chat_message:manage (to delete known Kick messages with /clear kick). It does not request stream-key or rewards access.`
}

func (w *Wizard) twitch(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, twitchInstructions(*c))
	var err error
	c.Twitch.ClientID, err = w.value("Twitch Client ID", c.Twitch.ClientID, false)
	if err != nil {
		return err
	}
	c.Twitch.ClientSecret, err = w.value("Twitch Client Secret", c.Twitch.ClientSecret, true)
	if err != nil {
		return err
	}
	res, err := w.Authorize(ctx, oauthpkg.Request{AuthorizeURL: strings.TrimRight(c.Twitch.OAuthBaseURL, "/") + "/authorize", ClientID: c.Twitch.ClientID, RedirectURI: c.Twitch.RedirectURI, Scopes: twitch.SetupScopes}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	api := &twitch.API{HTTP: w.HTTP, APIBaseURL: c.Twitch.APIBaseURL, OAuthBaseURL: c.Twitch.OAuthBaseURL, ClientID: c.Twitch.ClientID, ClientSecret: c.Twitch.ClientSecret}
	tok, err := api.Exchange(ctx, res.Code, c.Twitch.RedirectURI)
	if err != nil {
		return err
	}
	api.AccessToken = tok.AccessToken
	id, err := api.ValidateToken(ctx)
	if err != nil {
		return err
	}
	if err = twitch.RequireScopes(id.Scopes, twitch.SetupScopes...); err != nil {
		return err
	}
	c.Twitch.AccessToken = tok.AccessToken
	c.Twitch.RefreshToken = tok.RefreshToken
	c.Twitch.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	c.Twitch.UserID = id.UserID
	c.Twitch.UserLogin = id.Login
	ch, err := w.value("Twitch channel to monitor by default", c.Twitch.Channel, false)
	if err != nil {
		return err
	}
	c.Twitch.Channel, err = twitch.ParseChannel(ch)
	if err == nil {
		fmt.Fprintf(w.Out, "Authorized Twitch account %s.\n", id.Login)
	}
	return err
}

func (w *Wizard) twitchBot(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, twitchBotInstructions(*c))
	account := c.Bot.Twitch
	if account.ClientID == "" {
		account.ClientID = c.Twitch.ClientID
	}
	if account.ClientSecret == "" {
		account.ClientSecret = c.Twitch.ClientSecret
	}
	var err error
	account.ClientID, err = w.value("Twitch bot Client ID", account.ClientID, false)
	if err != nil {
		return err
	}
	account.ClientSecret, err = w.value("Twitch bot Client Secret", account.ClientSecret, true)
	if err != nil {
		return err
	}
	res, err := w.Authorize(ctx, oauthpkg.Request{
		AuthorizeURL: strings.TrimRight(c.Twitch.OAuthBaseURL, "/") + "/authorize",
		ClientID:     account.ClientID,
		RedirectURI:  c.Twitch.RedirectURI,
		Scopes:       twitch.RequiredChatScopes,
	}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	api := &twitch.API{
		HTTP: w.HTTP, APIBaseURL: c.Twitch.APIBaseURL, OAuthBaseURL: c.Twitch.OAuthBaseURL,
		ClientID: account.ClientID, ClientSecret: account.ClientSecret,
	}
	tok, err := api.Exchange(ctx, res.Code, c.Twitch.RedirectURI)
	if err != nil {
		return err
	}
	api.AccessToken = tok.AccessToken
	identity, err := api.ValidateToken(ctx)
	if err != nil {
		return err
	}
	if err = twitch.RequireScopes(identity.Scopes, twitch.RequiredChatScopes...); err != nil {
		return err
	}
	if sameTwitchIdentity(c.Twitch, identity) {
		return fmt.Errorf("Twitch authorized the primary account %s, not a separate bot account; switch the browser to the intended bot account and rerun: streamchat setup twitch-bot", identity.Login)
	}
	account.AccessToken = tok.AccessToken
	account.RefreshToken = tok.RefreshToken
	account.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	account.UserID = identity.UserID
	account.UserLogin = identity.Login
	c.Bot.Twitch = account
	fmt.Fprintf(w.Out, "Authorized dedicated Twitch bot account %s for channel %s.\n", identity.Login, c.Twitch.Channel)
	return nil
}

func sameTwitchIdentity(primary config.Twitch, candidate twitch.Identity) bool {
	return primary.UserID != "" && primary.UserID == candidate.UserID ||
		primary.UserLogin != "" && strings.EqualFold(primary.UserLogin, candidate.Login)
}

func twitchInstructions(c config.Config) string {
	return `Twitch chat uses the official EventSub WebSocket API; no public webhook is needed.

1. Register an application at https://dev.twitch.tv/console/apps
2. Add this exact OAuth redirect URI:
   ` + c.Twitch.RedirectURI + `
3. The Client ID identifies your app. The Client Secret authenticates the local authorization-code exchange and must remain private.
4. Your browser will ask for exactly user:read:chat, user:write:chat, channel:manage:broadcast, moderator:manage:banned_users, and moderator:manage:chat_messages. These allow Streamchat to receive channel.chat.message events, send chat messages, update the authenticated broadcaster's title/category, ban or timeout users when the account is a channel moderator, and clear or delete Twitch chat messages. Streamchat stores the user access token and refresh token privately and refreshes authorization when required.

The Twitch account you authorize is the identity Streamchat reads and sends chat as and the moderator identity used by moderation APIs. Channel names are resolved to numeric IDs automatically. Existing authorizations continue to work for the features covered by their current scopes; only a command missing its required scope asks you to rerun streamchat setup twitch.`
}

func twitchBotInstructions(c config.Config) string {
	return `This authorizes a separate Twitch identity for Streamchat's bot replies.

1. Keep the broadcaster account and channel configuration unchanged.
2. Before approving the browser prompt, make sure Twitch is signed in as the intended bot account.
3. Streamchat requests only user:read:chat and user:write:chat for that bot identity.
4. The bot authorization uses this loopback redirect URI:
   ` + c.Twitch.RedirectURI + `

The existing Twitch application may be reused. The bot's OAuth tokens and resolved account identity are stored under bot.twitch; the primary twitch account remains untouched.`
}

func ParsePlatforms(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args) > 1 {
		return nil, errors.New("setup accepts at most one platform")
	}
	switch args[0] {
	case "youtube", "youtube-server", "kick", "twitch", "twitch-bot":
		return []string{args[0]}, nil
	default:
		return nil, fmt.Errorf("unknown platform %q", args[0])
	}
}

func NumericChoice(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }
