package setup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SleepyMario/streamchat/internal/config"
	oauthpkg "github.com/SleepyMario/streamchat/internal/oauth"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
)

func TestSelectionParsing(t *testing.T) {
	got, err := ParseSelection("1, twitch 1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"youtube", "twitch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if _, err = ParseSelection(""); !errors.Is(err, ErrCancelled) {
		t.Fatalf("%v", err)
	}
	if _, err = ParseSelection("instagram"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
}

func TestCancelledSetupDoesNotCreateOrChangeConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	w := New(strings.NewReader("\n"), &out, p)
	if err := w.Run(context.Background(), nil); !errors.Is(err, ErrCancelled) {
		t.Fatalf("%v", err)
	}
	if _, err := config.Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestYouTubeSetupPreservesOtherPlatformsAndExistingSecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := config.Defaults()
	c.YouTube.APIKey = "old-key"
	c.Kick.ClientID = "kick-existing"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := New(strings.NewReader("y\n"), &out, p)
	if err := w.Run(context.Background(), []string{"youtube"}); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.YouTube.APIKey != "old-key" || got.Kick.ClientID != "kick-existing" {
		t.Fatalf("existing values overwritten: %+v", got)
	}
	if strings.Contains(out.String(), "old-key") {
		t.Fatal("secret printed")
	}
}

func TestSecretInputTrimsSurroundingWhitespace(t *testing.T) {
	var out bytes.Buffer
	w := New(strings.NewReader("  pasted-secret\r\n"), &out, "")
	got, err := w.value("Secret", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "pasted-secret" {
		t.Fatalf("secret input was not normalized: %q", got)
	}
}

func TestKickInstructionsExplainPortalWebhookSourceOfTruth(t *testing.T) {
	c := config.Defaults()
	text := kickInstructions(c)
	for _, want := range []string{
		"http://localhost:8789/oauth/kick/callback",
		"Enter that same portal webhook URL below as kick.webhook_url",
		"does not send it in the Kick event-subscription request",
		"Changing the JSON value alone does not change Kick's webhook destination",
		"streamchat kick subscribe",
		"chat:write",
		"channel:write",
		"channel:read",
		"moderation:ban",
		"moderation:chat_message:manage",
		"does not request stream-key or rewards access",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kick instructions missing %q:\n%s", want, text)
		}
	}
}

func TestKickOAuthScopesIncludeOnlyRequiredCapabilities(t *testing.T) {
	want := []string{"user:read", "events:subscribe", kick.ChatWriteScope, kick.ChannelWriteScope, kick.ChannelReadScope, kick.ModerationBanScope, kick.ChatMessageManageScope}
	if !reflect.DeepEqual(kickOAuthScopes, want) {
		t.Fatalf("scopes=%v want=%v", kickOAuthScopes, want)
	}
}

func TestKickBotOAuthScopesIncludeOnlyIdentityAndChat(t *testing.T) {
	want := []string{"user:read", kick.ChatWriteScope}
	if !reflect.DeepEqual(kickBotOAuthScopes, want) {
		t.Fatalf("scopes=%v want=%v", kickBotOAuthScopes, want)
	}
}

func TestTwitchOAuthScopesIncludeOnlyRequiredCapabilities(t *testing.T) {
	want := []string{twitch.ReadChatScope, twitch.WriteChatScope, twitch.ManageBroadcastScope, twitch.ManageBannedUsersScope, twitch.ManageChatMessagesScope, twitch.ReadFollowersScope, twitch.ReadSubscriptionsScope}
	if !reflect.DeepEqual(twitch.SetupScopes, want) {
		t.Fatalf("scopes=%v want=%v", twitch.SetupScopes, want)
	}
}

func TestYouTubeOAuthScopeSupportsChatAndModeration(t *testing.T) {
	want := []string{"https://www.googleapis.com/auth/youtube.force-ssl"}
	if !reflect.DeepEqual(youtubeServerScopes, want) {
		t.Fatalf("scopes=%v want=%v", youtubeServerScopes, want)
	}
}

func TestTwitchInstructionsExplainFeatureScopedReauthorization(t *testing.T) {
	text := twitchInstructions(config.Defaults())
	for _, want := range []string{
		"http://localhost:8790/oauth/twitch/callback",
		"exactly user:read:chat, user:write:chat, channel:manage:broadcast, moderator:manage:banned_users, moderator:manage:chat_messages, moderator:read:followers, and channel:read:subscriptions",
		"receive chat, follow, subscription and Bits activity",
		"ban or timeout users when the account is a channel moderator",
		"clear or delete Twitch chat messages",
		"Authorizations created before Streamchat 3.4 must be renewed once",
		"streamchat setup twitch",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Twitch instructions missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"channel:bot", "user:bot", "channel:manage:moderators"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("Twitch instructions contain unrelated scope %q:\n%s", unwanted, text)
		}
	}
}

func TestTwitchBotInstructionsDescribeDedicatedMinimalAuthorization(t *testing.T) {
	text := twitchBotInstructions(config.Defaults())
	for _, want := range []string{
		"separate Twitch identity",
		"signed in as the intended bot account",
		"only user:read:chat and user:write:chat",
		"http://localhost:8790/oauth/twitch/callback",
		"primary twitch account remains untouched",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Twitch bot instructions missing %q:\n%s", want, text)
		}
	}
}

func TestTwitchBotSetupUsesMinimalScopesAndPreservesPrimaryIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != "shared-client" || r.Form.Get("client_secret") != "shared-secret" || r.Form.Get("code") != "bot-code" {
				t.Fatalf("unexpected token exchange: %v", r.Form)
			}
			_, _ = io.WriteString(w, `{"access_token":"bot-access","refresh_token":"bot-refresh","expires_in":3600,"scope":["user:read:chat","user:write:chat"]}`)
		case "/validate":
			if r.Header.Get("Authorization") != "OAuth bot-access" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"client_id":"shared-client","user_id":"bot-user","login":"comradekip","scopes":["user:read:chat","user:write:chat"],"expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := config.Defaults()
	c.Twitch.ClientID = "shared-client"
	c.Twitch.ClientSecret = "shared-secret"
	c.Twitch.AccessToken = "owner-access"
	c.Twitch.RefreshToken = "owner-refresh"
	c.Twitch.UserID = "owner-user"
	c.Twitch.UserLogin = "sleepymario"
	c.Twitch.Channel = "sleepymario"
	c.Twitch.OAuthBaseURL = server.URL
	var out bytes.Buffer
	wizard := New(strings.NewReader("y\ny\n"), &out, "")
	wizard.HTTP = server.Client()
	wizard.Authorize = func(_ context.Context, request oauthpkg.Request, _ io.Writer, _ func(string) error) (oauthpkg.Result, error) {
		if !reflect.DeepEqual(request.Scopes, twitch.RequiredChatScopes) {
			t.Fatalf("scopes=%v", request.Scopes)
		}
		return oauthpkg.Result{Code: "bot-code"}, nil
	}
	if err := wizard.twitchBot(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Twitch.AccessToken != "owner-access" || c.Twitch.RefreshToken != "owner-refresh" || c.Twitch.UserLogin != "sleepymario" {
		t.Fatalf("primary identity changed: %+v", c.Twitch)
	}
	if c.Bot.Twitch.ClientID != "shared-client" || c.Bot.Twitch.AccessToken != "bot-access" || c.Bot.Twitch.RefreshToken != "bot-refresh" || c.Bot.Twitch.UserID != "bot-user" || c.Bot.Twitch.UserLogin != "comradekip" {
		t.Fatalf("bot identity not saved: %+v", c.Bot.Twitch)
	}
	if strings.Contains(out.String(), "shared-secret") || strings.Contains(out.String(), "bot-access") || strings.Contains(out.String(), "bot-refresh") {
		t.Fatalf("secret appeared in output: %s", out.String())
	}
}

func TestTwitchBotSetupRejectsPrimaryIdentity(t *testing.T) {
	primary := config.Twitch{UserID: "owner-user", UserLogin: "SleepyMario"}
	if !sameTwitchIdentity(primary, twitch.Identity{UserID: "owner-user", Login: "renamed-owner"}) {
		t.Fatal("same primary user ID was accepted as a bot")
	}
	if !sameTwitchIdentity(primary, twitch.Identity{UserID: "different", Login: "sleepymario"}) {
		t.Fatal("same primary login was accepted as a bot")
	}
	if sameTwitchIdentity(primary, twitch.Identity{UserID: "bot-user", Login: "comradekip"}) {
		t.Fatal("dedicated bot identity was rejected")
	}
}

func TestKickBotInstructionsDescribeDedicatedMinimalAuthorization(t *testing.T) {
	text := kickBotInstructions(config.Defaults())
	for _, want := range []string{
		"separate Kick identity",
		"signed in as the intended bot account",
		"only user:read and chat:write",
		"http://localhost:8789/oauth/kick/callback",
		"primary kick account and event subscriptions remain untouched",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kick bot instructions missing %q:\n%s", want, text)
		}
	}
}

func TestKickBotSetupUsesMinimalScopesAndPreservesPrimaryIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != "shared-client" || r.Form.Get("client_secret") != "shared-secret" || r.Form.Get("code") != "bot-code" || r.Form.Get("code_verifier") != "bot-verifier" {
				t.Fatalf("unexpected token exchange: %v", r.Form)
			}
			_, _ = io.WriteString(w, `{"access_token":"bot-access","refresh_token":"bot-refresh","expires_in":3600,"scope":"user:read chat:write"}`)
		case "/users":
			if r.Header.Get("Authorization") != "Bearer bot-access" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"data":[{"user_id":2002,"name":"ComradeKip"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := config.Defaults()
	c.Kick.ClientID = "shared-client"
	c.Kick.ClientSecret = "shared-secret"
	c.Kick.AccessToken = "owner-access"
	c.Kick.RefreshToken = "owner-refresh"
	c.Kick.BroadcasterID = "1001"
	c.Kick.OAuthBaseURL = server.URL
	c.Kick.APIBaseURL = server.URL
	var out bytes.Buffer
	wizard := New(strings.NewReader("y\ny\n"), &out, "")
	wizard.HTTP = server.Client()
	wizard.Authorize = func(_ context.Context, request oauthpkg.Request, _ io.Writer, _ func(string) error) (oauthpkg.Result, error) {
		if !reflect.DeepEqual(request.Scopes, kickBotOAuthScopes) || !request.UsePKCE {
			t.Fatalf("authorization request=%+v", request)
		}
		return oauthpkg.Result{Code: "bot-code", Verifier: "bot-verifier"}, nil
	}
	if err := wizard.kickBot(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Kick.AccessToken != "owner-access" || c.Kick.RefreshToken != "owner-refresh" || c.Kick.BroadcasterID != "1001" {
		t.Fatalf("primary identity changed: %+v", c.Kick)
	}
	if c.Bot.Kick.ClientID != "shared-client" || c.Bot.Kick.AccessToken != "bot-access" || c.Bot.Kick.RefreshToken != "bot-refresh" || c.Bot.Kick.UserID != "2002" || c.Bot.Kick.UserLogin != "ComradeKip" {
		t.Fatalf("bot identity not saved: %+v", c.Bot.Kick)
	}
	if strings.Contains(out.String(), "shared-secret") || strings.Contains(out.String(), "bot-access") || strings.Contains(out.String(), "bot-refresh") {
		t.Fatalf("secret appeared in output: %s", out.String())
	}
}

func TestKickBotSetupRejectsPrimaryIdentity(t *testing.T) {
	primary := config.Kick{BroadcasterID: "1001"}
	if !sameKickIdentity(primary, "1001") {
		t.Fatal("same primary user ID was accepted as a bot")
	}
	if sameKickIdentity(primary, "2002") {
		t.Fatal("dedicated bot identity was rejected")
	}
}
