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
var youtubeBotScopes = []string{youtube.ManageScope}

type Wizard struct {
	In              *bufio.Reader
	rawIn           io.Reader
	Out             io.Writer
	Path            string
	HTTP            *http.Client
	YouTubeTokenURL string
	OpenBrowser     func(string) error
	Authorize       AuthorizeFunc
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
		case "youtube-bot":
			err = w.youtubeBot(ctx, &c)
		case "kick":
			err = w.kick(ctx, &c)
		case "kick-bot":
			err = w.kickBot(ctx, &c)
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
		} else if name == "youtube-bot" {
			display = "YouTube bot"
		} else if name == "kick-bot" {
			display = "Kick bot"
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
	oc := youtube.OAuthClient{HTTP: w.HTTP, TokenURL: w.YouTubeTokenURL, ClientID: c.YouTube.ClientID, ClientSecret: c.YouTube.ClientSecret}
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

func (w *Wizard) youtubeBot(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, youtubeBotInstructions(*c))
	account := c.Bot.YouTube
	if account.ClientID == "" {
		account.ClientID = c.YouTube.ClientID
	}
	if account.ClientSecret == "" {
		account.ClientSecret = c.YouTube.ClientSecret
	}
	var err error
	account.ClientID, err = w.value("YouTube bot OAuth Client ID", account.ClientID, false)
	if err != nil {
		return err
	}
	account.ClientSecret, err = w.value("YouTube bot OAuth Client Secret", account.ClientSecret, true)
	if err != nil {
		return err
	}
	result, err := w.Authorize(ctx, oauthpkg.Request{
		AuthorizeURL: youtube.AuthorizeURL,
		ClientID:     account.ClientID,
		RedirectURI:  c.YouTube.RedirectURI,
		Scopes:       youtubeBotScopes,
		UsePKCE:      true,
		Parameters: map[string]string{
			"access_type":            "offline",
			"include_granted_scopes": "true",
			"prompt":                 "consent select_account",
		},
	}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	oauthClient := youtube.OAuthClient{HTTP: w.HTTP, TokenURL: w.YouTubeTokenURL, ClientID: account.ClientID, ClientSecret: account.ClientSecret}
	token, err := oauthClient.Exchange(ctx, result.Code, c.YouTube.RedirectURI, result.Verifier)
	if err != nil {
		return err
	}
	if token.RefreshToken == "" {
		return errors.New("Google did not return a bot refresh token; revoke the prior grant if necessary, then run: streamchat setup youtube-bot")
	}
	botClient := youtube.New(w.HTTP, c.YouTube.BaseURL, "", token.AccessToken, "")
	botClient.ClientID = account.ClientID
	botClient.ClientSecret = account.ClientSecret
	botClient.RefreshToken = token.RefreshToken
	botClient.TokenExpiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	botID, botName, err := botClient.CurrentChannel(ctx)
	if err != nil {
		return fmt.Errorf("resolve YouTube bot identity: %w", err)
	}
	ownerClient := youtube.New(w.HTTP, c.YouTube.BaseURL, c.YouTube.APIKey, c.YouTube.AccessToken, "")
	ownerClient.ClientID = c.YouTube.ClientID
	ownerClient.ClientSecret = c.YouTube.ClientSecret
	ownerClient.RefreshToken = c.YouTube.RefreshToken
	ownerClient.TokenExpiry = c.YouTube.TokenExpiry
	ownerClient.OnToken = func(refreshed youtube.Token) error {
		c.YouTube.AccessToken = refreshed.AccessToken
		c.YouTube.RefreshToken = refreshed.RefreshToken
		c.YouTube.TokenExpiry = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
		return nil
	}
	ownerID, _, err := ownerClient.CurrentChannel(ctx)
	if err != nil {
		return fmt.Errorf("verify primary YouTube identity before storing the bot: %w", err)
	}
	if sameYouTubeIdentity(ownerID, botID) {
		return fmt.Errorf("YouTube authorized the primary channel %s, not a separate bot channel; choose the intended bot account and rerun: streamchat setup youtube-bot", botName)
	}
	account.AccessToken = token.AccessToken
	account.RefreshToken = token.RefreshToken
	account.TokenExpiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	account.UserID = botID
	account.UserLogin = botName
	c.Bot.YouTube = account
	fmt.Fprintf(w.Out, "Authorized dedicated YouTube bot channel %s for replies on SleepyMario's live broadcasts.\n", botName)
	return nil
}

func sameYouTubeIdentity(primaryChannelID, candidateChannelID string) bool {
	return primaryChannelID != "" && primaryChannelID == candidateChannelID
}

func youtubeBotInstructions(c config.Config) string {
	return `This authorizes a separate YouTube channel for Streamchat's bot replies.

1. Keep SleepyMario's primary YouTube authorization unchanged.
2. Choose the Google account and YouTube channel intended to appear as ComradeKip.
3. The bot authorization uses this loopback redirect URI:
   ` + c.YouTube.RedirectURI + `
4. At your request, Streamchat asks for Google's broad youtube scope (Manage your YouTube account), not a least-privilege chat-only grant. The 3.8 implementation itself uses it only to identify the bot channel and post live-chat replies, but the OAuth grant is broader and may authorize destructive operations on YouTube content if such code is added later or the credential is compromised.

The existing Google OAuth Desktop application may be reused. The bot tokens and resolved channel identity are stored under bot.youtube; the primary YouTube credentials remain separate.`
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
var kickBotOAuthScopes = []string{"user:read", kick.ChatWriteScope}

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
	fmt.Fprintln(w.Out, "Kick chat, follow, subscription, gift-subscription and KICKs event subscriptions created. Delivery uses the webhook URL in the Kick developer portal; the local kick.webhook_url value was not sent.")
	return nil
}

func (w *Wizard) kickBot(ctx context.Context, c *config.Config) error {
	fmt.Fprintln(w.Out, kickBotInstructions(*c))
	account := c.Bot.Kick
	if account.ClientID == "" {
		account.ClientID = c.Kick.ClientID
	}
	if account.ClientSecret == "" {
		account.ClientSecret = c.Kick.ClientSecret
	}
	var err error
	account.ClientID, err = w.value("Kick bot Client ID", strings.TrimSpace(account.ClientID), false)
	if err != nil {
		return err
	}
	account.ClientSecret, err = w.value("Kick bot Client Secret", strings.TrimSpace(account.ClientSecret), true)
	if err != nil {
		return err
	}
	res, err := w.Authorize(ctx, oauthpkg.Request{
		AuthorizeURL: strings.TrimRight(c.Kick.OAuthBaseURL, "/") + "/oauth/authorize",
		ClientID:     account.ClientID,
		RedirectURI:  c.Kick.RedirectURI,
		Scopes:       kickBotOAuthScopes,
		UsePKCE:      true,
	}, w.Out, w.OpenBrowser)
	if err != nil {
		return err
	}
	oc := kick.OAuthClient{
		HTTP: w.HTTP, OAuthBaseURL: c.Kick.OAuthBaseURL, APIBaseURL: c.Kick.APIBaseURL,
		ClientID: account.ClientID, ClientSecret: account.ClientSecret,
	}
	tok, err := oc.Exchange(ctx, res.Code, c.Kick.RedirectURI, res.Verifier)
	if err != nil {
		return err
	}
	id, name, err := oc.CurrentUser(ctx, tok.AccessToken)
	if err != nil {
		return err
	}
	if sameKickIdentity(c.Kick, id) {
		return fmt.Errorf("Kick authorized the primary account %s, not a separate bot account; switch the browser to the intended bot account and rerun: streamchat setup kick-bot", name)
	}
	account.AccessToken = tok.AccessToken
	account.RefreshToken = tok.RefreshToken
	account.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	account.UserID = id
	account.UserLogin = name
	c.Bot.Kick = account
	fmt.Fprintf(w.Out, "Authorized dedicated Kick bot account %s for broadcaster user ID %s.\n", name, c.Kick.BroadcasterID)
	return nil
}

func sameKickIdentity(primary config.Kick, candidateUserID string) bool {
	return primary.BroadcasterID != "" && primary.BroadcasterID == candidateUserID
}

func kickInstructions(c config.Config) string {
	return `Kick delivers official chat, follow, subscription, gift-subscription and KICKs events to a verified webhook.

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
4. Your browser will ask for exactly user:read:chat, user:write:chat, channel:manage:broadcast, moderator:manage:banned_users, moderator:manage:chat_messages, moderator:read:followers, and channel:read:subscriptions. These allow Streamchat to receive chat, follow, subscription and Bits activity; send chat messages; update the authenticated broadcaster's title/category; ban or timeout users when the account is a channel moderator; and clear or delete Twitch chat messages. Streamchat stores the user access token and refresh token privately and refreshes authorization when required.

The Twitch account you authorize is the identity Streamchat reads and sends chat as and the moderator identity used by moderation APIs. Channel names are resolved to numeric IDs automatically. Authorizations created before Streamchat 3.4 must be renewed once with streamchat setup twitch to enable the new follow and subscription alerts.`
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

func kickBotInstructions(c config.Config) string {
	return `This authorizes a separate Kick identity for Streamchat's bot replies.

1. Keep the broadcaster account, broadcaster user ID and webhook configuration unchanged.
2. Before approving the browser prompt, make sure Kick is signed in as the intended bot account.
3. Streamchat requests only user:read and chat:write for that bot identity.
4. The bot authorization uses this loopback redirect URI:
   ` + c.Kick.RedirectURI + `

The existing Kick application may be reused. The bot's OAuth tokens and resolved account identity are stored under bot.kick; the primary kick account and event subscriptions remain untouched.`
}

func ParsePlatforms(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args) > 1 {
		return nil, errors.New("setup accepts at most one platform")
	}
	switch args[0] {
	case "youtube", "youtube-server", "youtube-bot", "kick", "kick-bot", "twitch", "twitch-bot":
		return []string{args[0]}, nil
	default:
		return nil, fmt.Errorf("unknown platform %q", args[0])
	}
}

func NumericChoice(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }
