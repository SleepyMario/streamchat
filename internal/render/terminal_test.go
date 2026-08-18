package render

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/chattercolor"
	"github.com/SleepyMario/streamchat/internal/emote"
	"github.com/SleepyMario/streamchat/internal/platform/kick"
)

func TestSanitizeAndUnicode(t *testing.T) {
	s := Sanitize("héllo\x1b[31mBAD\x1b[0m\x07\nnext")
	if s != "hélloBAD next" {
		t.Fatalf("%q", s)
	}
}
func TestNoColorOutput(t *testing.T) {
	var b bytes.Buffer
	m := chat.Message{ID: "1", Platform: chat.PlatformKick, Timestamp: time.Now(), AuthorDisplayName: "名", Text: "ok", EventType: chat.EventMessage}
	if e := New(&b, Options{Color: false}).Render(m); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(b.String(), "\x1b") || !strings.Contains(b.String(), "名") {
		t.Fatal(b.String())
	}
}
func TestNOColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(false) {
		t.Fatal("NO_COLOR ignored")
	}
	line := New(io.Discard, Options{
		Color:              ColorEnabled(false),
		ChatColorMode:      chattercolor.ModeLine,
		ChatterColorSource: chattercolor.NewAllocator(),
	}).Format(chat.Message{Platform: chat.PlatformKick, AuthorID: "1", AuthorDisplayName: "viewer", Text: "hello"})
	for _, color := range chattercolor.Palette() {
		if strings.Contains(line.Text, color.ANSI) {
			t.Fatalf("NO_COLOR output used %s: %q", color.Name, line.Text)
		}
	}
}

func TestChatterColorModes(t *testing.T) {
	message := chat.Message{Platform: chat.PlatformKick, AuthorID: "42", AuthorDisplayName: "Alice", Roles: roles(chat.RoleModerator), Text: "hello"}
	cyan := chattercolor.Palette()[0].ANSI

	line := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	if !strings.HasPrefix(line.Text, cyan+"[KICK] [M] Alice") || !strings.HasSuffix(line.Text, "hello\x1b[0m") {
		t.Fatalf("line mode=%q", line.Text)
	}
	if strings.Contains(strings.TrimPrefix(line.Text, cyan), "\x1b[32m") {
		t.Fatalf("provider color interrupted whole-line color: %q", line.Text)
	}

	username := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeUsername, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	if !strings.Contains(username.Text, "\x1b[32m[KICK]\x1b[0m [M] "+cyan+"Alice\x1b[0m") || strings.Contains(username.Text, cyan+"hello") || !strings.HasSuffix(username.Text, "\x1b[0m") {
		t.Fatalf("username mode=%q", username.Text)
	}

	off := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeOff, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	wantOff := New(io.Discard, Options{Color: true}).Format(message)
	if off.Text != wantOff.Text {
		t.Fatalf("off=%q current=%q", off.Text, wantOff.Text)
	}
}

func TestChatterColorsRequireColorSupportAndActualChatter(t *testing.T) {
	allocator := chattercolor.NewAllocator()
	options := Options{Color: false, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: allocator}
	plain := New(io.Discard, options).Format(chat.Message{Platform: chat.PlatformKick, AuthorID: "1", AuthorDisplayName: "viewer", Text: "hello"})
	if strings.Contains(plain.Text, "\x1b[") {
		t.Fatalf("--no-color output=%q", plain.Text)
	}

	system := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: allocator}).Format(chat.Message{Platform: chat.PlatformKick, Text: "local status"})
	for _, color := range chattercolor.Palette() {
		if strings.Contains(system.Text, color.ANSI) {
			t.Fatalf("system message received chatter color: %q", system.Text)
		}
	}
}

func TestChatterANSILeavesUnicodeAlignmentAndEmoteColumnsUnchanged(t *testing.T) {
	resolve := func(chat.Platform, chat.Emote) (string, bool) { return "/tmp/emote.img", true }
	message := chat.Message{
		Platform:          chat.PlatformKick,
		AuthorID:          "7",
		AuthorDisplayName: "聊天室",
		Text:              "你好 [emote:7:WAVE]",
		Emotes:            []chat.Emote{{ID: "7", Name: "WAVE", URL: "https://example.invalid/emote", Start: 3, End: 16}},
	}
	base := New(io.Discard, Options{Emotes: resolve}).Format(message)
	colored := New(io.Discard, Options{Color: true, Emotes: resolve, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	if len(base.Images) != 1 || len(colored.Images) != 1 || base.Images[0].Column != colored.Images[0].Column {
		t.Fatalf("base=%+v colored=%+v", base.Images, colored.Images)
	}
	if Sanitize(colored.Text) != base.Text || Sanitize(colored.GraphicalText) != base.GraphicalText {
		t.Fatalf("ANSI changed display content: base=%q colored=%q", base.Text, colored.Text)
	}
}

func TestRedrawAndRepeatedChatterDoNotAdvanceAssignment(t *testing.T) {
	allocator := chattercolor.NewAllocator()
	formatter := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: allocator})
	first := chat.Message{Platform: chat.PlatformKick, AuthorID: "1", AuthorDisplayName: "first", Text: "one"}
	for range 5 {
		if got := formatter.Format(first).Text; !strings.HasPrefix(got, chattercolor.Palette()[0].ANSI) {
			t.Fatalf("first chatter changed color on redraw: %q", got)
		}
	}
	second := formatter.Format(chat.Message{Platform: chat.PlatformKick, AuthorID: "2", AuthorDisplayName: "second", Text: "two"})
	if !strings.HasPrefix(second.Text, chattercolor.Palette()[1].ANSI) {
		t.Fatalf("redraw advanced allocator: %q", second.Text)
	}
}

func TestStructuredEmotesUseReadableTextFallbackWithoutImageBackend(t *testing.T) {
	var output bytes.Buffer
	message := chat.Message{Platform: chat.PlatformKick, AuthorDisplayName: "viewer", Text: "hello [emote:7:ppJedi]", Emotes: []chat.Emote{{ID: "7", Name: "ppJedi", Start: 6, End: 21}}}
	if err := New(&output, Options{}).Render(message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "hello :ppJedi:") || strings.Contains(output.String(), "[emote:") {
		t.Fatalf("non-TTY fallback=%q", output.String())
	}
}

func TestTwitchRolesChatterIDsCJKAndEmoteFallbackRenderTogether(t *testing.T) {
	allocator := chattercolor.NewAllocator()
	formatter := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: allocator})
	message := chat.Message{
		Platform:          chat.PlatformTwitch,
		AuthorID:          "twitch-user-1",
		AuthorDisplayName: "觀眾",
		Roles:             roles(chat.RoleBroadcaster, chat.RoleModerator, chat.RolePartner, chat.RoleVIP, chat.RoleSubscriber),
		Text:              "你好 Kappa",
		Emotes:            []chat.Emote{{ID: "25", Name: "Kappa", Start: 3, End: 7}},
	}
	first := formatter.Format(message)
	if !strings.HasPrefix(first.Text, chattercolor.Palette()[0].ANSI) || !strings.Contains(Sanitize(first.Text), "[TW]") || !strings.Contains(Sanitize(first.Text), "[B][M][P][V][S] 觀眾") || !strings.Contains(Sanitize(first.Text), "你好 Kappa") {
		t.Fatalf("first=%q", first.Text)
	}
	message.AuthorDisplayName = "renamed"
	repeated := formatter.Format(message)
	if !strings.HasPrefix(repeated.Text, chattercolor.Palette()[0].ANSI) {
		t.Fatalf("Twitch chatter ID color changed: %q", repeated.Text)
	}
	second := formatter.Format(chat.Message{Platform: chat.PlatformTwitch, AuthorID: "twitch-user-2", AuthorDisplayName: "second", Text: "hello"})
	if !strings.HasPrefix(second.Text, chattercolor.Palette()[1].ANSI) {
		t.Fatalf("second Twitch chatter color=%q", second.Text)
	}
}

type availableImageBackend struct{}

func (availableImageBackend) Available() bool          { return true }
func (availableImageBackend) Update([]emote.Placement) {}
func (availableImageBackend) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestOlderKickRelayEmoteReachesCacheAndBecomesImage(t *testing.T) {
	directory := t.TempDir()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != "https://files.kick.com/emotes/1730755/fullsize" {
			t.Fatalf("URL=%s", request.URL)
		}
		var asset bytes.Buffer
		picture := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Transparent, color.White})
		if err := gif.Encode(&asset, picture, nil); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/gif"}}, Body: io.NopCloser(bytes.NewReader(asset.Bytes()))}, nil
	})}
	cache, err := emote.NewCache(emote.CacheOptions{Directory: directory, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	controller := emote.NewController(emote.ControllerOptions{Mode: "auto", Cache: cache, Backend: availableImageBackend{}})
	redrawn := make(chan struct{}, 1)
	controller.SetRedraw(func() { redrawn <- struct{}{} })
	message := chat.Message{Platform: chat.PlatformKick, AuthorDisplayName: "viewer", Text: "[emote:1730755:ppJedi]", Emotes: []chat.Emote{{ID: "1730755", Start: 0, End: 21}}}
	message.Emotes = kick.EnrichEmotes(message.Text, message.Emotes)
	formatter := New(io.Discard, Options{Emotes: controller.Resolve})
	if first := formatter.Format(message); !strings.Contains(first.Text, ":ppJedi:") {
		t.Fatalf("first render=%+v", first)
	}
	select {
	case <-redrawn:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not trigger redraw")
	}
	second := formatter.Format(message)
	if len(second.Images) != 1 || requests != 1 {
		t.Fatalf("second=%+v requests=%d", second, requests)
	}
	if _, err = os.Stat(filepath.Join(directory, "kick", "1730755.img")); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}

func TestIdentityRendering(t *testing.T) {
	tests := []struct {
		name        string
		author      string
		roles       chat.RoleSet
		authorWidth int
		want        string
	}{
		{
			name:   "normal username alignment",
			author: "SleepyMario",
			want:   "[KICK] SleepyMario       hello\n",
		},
		{
			name:   "moderator letter before username",
			author: "BotRix",
			roles:  roles(chat.RoleModerator),
			want:   "[KICK] [M] BotRix        hello\n",
		},
		{
			name:   "broadcaster before username",
			author: "SleepyMario",
			roles:  roles(chat.RoleBroadcaster),
			want:   "[KICK] [B] SleepyMario   hello\n",
		},
		{
			name:   "moderator subscriber",
			author: "SomeUser",
			roles:  roles(chat.RoleSubscriber, chat.RoleModerator),
			want:   "[KICK] [M][S] SomeUser   hello\n",
		},
		{
			name:   "broadcaster moderator",
			author: "Owner",
			roles:  roles(chat.RoleModerator, chat.RoleBroadcaster),
			want:   "[KICK] [B][M] Owner      hello\n",
		},
		{
			name:   "vip og subscriber",
			author: "Another",
			roles:  roles(chat.RoleSubscriber, chat.RoleOG, chat.RoleVIP),
			want:   "[KICK] [V][O][S] Another  hello\n",
		},
		{
			name:   "long username exceeding cap",
			author: "abcdefghijklmnopqrstuvwx",
			want:   "[KICK] abcdefghijklmnopqrstuvwx  hello\n",
		},
		{
			name:        "long moderator identity exceeding cap",
			author:      "abcdefghijklmnopq",
			authorWidth: 40,
			roles:       roles(chat.RoleModerator),
			want:        "[KICK] [M] abcdefghijklmnopq   hello\n",
		},
		{
			name:   "raw and verified badges are not roles",
			author: "user",
			want:   "[KICK] user              hello\n",
		},
		{
			name:        "unicode display width alignment with role",
			author:      "名",
			authorWidth: 10,
			roles:       roles(chat.RoleVIP),
			want:        "[KICK] [V] 名      hello\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			m := chat.Message{
				Platform:          chat.PlatformKick,
				AuthorDisplayName: tt.author,
				Roles:             tt.roles,
				Badges:            []chat.Badge{{Type: "verified", Text: "Verified Channel"}},
				Text:              "hello",
			}
			if err := New(&b, Options{AuthorWidth: tt.authorWidth}).Render(m); err != nil {
				t.Fatal(err)
			}
			if got := b.String(); got != tt.want {
				t.Fatalf("Render() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderRoleBadgesFixedOrderAndUnknownOmitted(t *testing.T) {
	got := RenderRoleBadges(roles(chat.RoleFollower, chat.RoleSubscriber, chat.RoleOG, chat.RoleVIP, chat.RolePartner, chat.RoleModerator, chat.RoleBroadcaster))
	if got != "[B][M][P][V][O][S][F]" {
		t.Fatalf("badges=%q", got)
	}
	var unknown chat.RoleSet
	unknown.Add(chat.Role(99))
	if got = RenderRoleBadges(unknown); got != "" {
		t.Fatalf("unknown role rendered: %q", got)
	}
}

func TestIdentityAlignmentIncludesRoleDisplayWidth(t *testing.T) {
	render := func(author string, roleSet chat.RoleSet) string {
		var output bytes.Buffer
		if err := New(&output, Options{}).Render(chat.Message{Platform: chat.PlatformKick, AuthorDisplayName: author, Roles: roleSet, Text: "message"}); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	plain := render("LongerName", 0)
	badged := render("User", roles(chat.RoleModerator, chat.RoleSubscriber))
	if strings.Index(plain, "message") != strings.Index(badged, "message") {
		t.Fatalf("identity columns differ: plain=%q badged=%q", plain, badged)
	}
}

func roles(values ...chat.Role) chat.RoleSet {
	var result chat.RoleSet
	for _, value := range values {
		result.Add(value)
	}
	return result
}
