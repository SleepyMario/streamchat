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
	"github.com/SleepyMario/streamchat/internal/platform/twitch"
	"github.com/mattn/go-runewidth"
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
	if !strings.HasPrefix(line.Text, cyan+"[KICK]") || !strings.Contains(line.Text, "[M]") || !strings.Contains(line.Text, "Alice") || !strings.HasSuffix(line.Text, "hello\x1b[0m") {
		t.Fatalf("line mode=%q", line.Text)
	}
	if strings.Contains(strings.TrimPrefix(line.Text, cyan), "\x1b[32m") {
		t.Fatalf("provider color interrupted whole-line color: %q", line.Text)
	}

	username := New(io.Discard, Options{Color: true, ChatColorMode: chattercolor.ModeUsername, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	if !strings.Contains(username.Text, "\x1b[32m[KICK]\x1b[0m") || !strings.Contains(username.Text, "[M]") || !strings.Contains(username.Text, cyan+"Alice\x1b[0m") || strings.Contains(username.Text, cyan+"hello") || !strings.HasSuffix(username.Text, "\x1b[0m") {
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
	if !strings.HasPrefix(first.Text, chattercolor.Palette()[0].ANSI) || !strings.Contains(Sanitize(first.Text), "[Twitch]") || !strings.Contains(Sanitize(first.Text), "[B][M][P][V]") || !strings.Contains(Sanitize(first.Text), "觀眾") || !strings.Contains(Sanitize(first.Text), "你好 Kappa") {
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

func TestTwitchTimestampAndCanonicalLabel(t *testing.T) {
	timestamp := time.Date(2026, 8, 19, 12, 34, 56, 0, time.UTC)
	line := New(io.Discard, Options{Timestamps: true}).Format(chat.Message{Platform: chat.PlatformTwitch, Timestamp: timestamp, AuthorDisplayName: "viewer", Text: "hello"})
	wantPrefix := timestamp.Local().Format("15:04:05") + " [Twitch]"
	if !strings.HasPrefix(Sanitize(line.Text), wantPrefix) || strings.Contains(line.Text, "[TW]") {
		t.Fatalf("line=%q want prefix=%q", line.Text, wantPrefix)
	}
}

func TestTwitchANSIAndCJKDoNotChangeGraphicalEmoteColumn(t *testing.T) {
	resolve := func(chat.Platform, chat.Emote) (string, bool) { return "/tmp/twitch-emote.img", true }
	message := chat.Message{
		Platform:          chat.PlatformTwitch,
		AuthorID:          "twitch-user",
		AuthorDisplayName: "聊天室",
		Text:              "你好 Kappa 🙂",
		Emotes:            []chat.Emote{{ID: "25", Name: "Kappa", URL: twitch.TwitchEmoteURL("25", []string{"static"}), Start: 3, End: 7}},
	}
	base := New(io.Discard, Options{Emotes: resolve}).Format(message)
	colored := New(io.Discard, Options{Color: true, Emotes: resolve, ChatColorMode: chattercolor.ModeLine, ChatterColorSource: chattercolor.NewAllocator()}).Format(message)
	if len(base.Images) != 1 || len(colored.Images) != 1 || base.Images[0].Column != colored.Images[0].Column || base.Images[0].Column <= 0 {
		t.Fatalf("base=%+v colored=%+v", base.Images, colored.Images)
	}
	if Sanitize(colored.Text) != base.Text || Sanitize(colored.GraphicalText) != base.GraphicalText || !strings.Contains(base.Text, "你好 Kappa 🙂") {
		t.Fatalf("ANSI changed Twitch presentation: base=%q colored=%q", base.Text, colored.Text)
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

func TestTwitchAnimatedEmoteReachesProviderCacheAndStaticPreview(t *testing.T) {
	directory := t.TempDir()
	requests := 0
	emoteID := "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49"
	emoteName := "twitchdevHyperPitchfork"
	wantURL := twitch.TwitchEmoteURL(emoteID, []string{"static", "animated"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != wantURL {
			t.Fatalf("URL=%s want=%s", request.URL, wantURL)
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
	message := chat.Message{Platform: chat.PlatformTwitch, AuthorDisplayName: "viewer", Text: "你好 " + emoteName, Emotes: []chat.Emote{{ID: emoteID, Name: emoteName, URL: wantURL, Start: 3, End: 3 + len([]rune(emoteName)) - 1}}}
	formatter := New(io.Discard, Options{Emotes: controller.Resolve})
	first := formatter.Format(message)
	if !strings.Contains(first.Text, "你好 "+emoteName) || len(first.Images) != 0 {
		t.Fatalf("first render=%+v", first)
	}
	select {
	case <-redrawn:
	case <-time.After(2 * time.Second):
		t.Fatal("Twitch emote download did not trigger redraw")
	}
	second := formatter.Format(message)
	if len(second.Images) != 1 || !strings.Contains(second.Text, emoteName) || second.GraphicalText == "" || requests != 1 {
		t.Fatalf("second=%+v requests=%d", second, requests)
	}
	if !strings.HasSuffix(second.Images[0].Path, filepath.Join("twitch", emoteID+".static.png")) {
		t.Fatalf("preview path=%q", second.Images[0].Path)
	}
	if _, err = os.Stat(filepath.Join(directory, "twitch", emoteID+".img")); err != nil {
		t.Fatalf("cached source missing: %v", err)
	}
}

func TestIdentityRendering(t *testing.T) {
	tests := []struct {
		name        string
		author      string
		roles       chat.RoleSet
		authorWidth int
		wantAuthor  string
	}{
		{
			name:       "normal username alignment",
			author:     "SleepyMario",
			wantAuthor: "SleepyMario",
		},
		{
			name:       "moderator letter before username",
			author:     "BotRix",
			roles:      roles(chat.RoleModerator),
			wantAuthor: "BotRix",
		},
		{
			name:       "broadcaster before username",
			author:     "SleepyMario",
			roles:      roles(chat.RoleBroadcaster),
			wantAuthor: "SleepyMario",
		},
		{
			name:       "moderator subscriber",
			author:     "SomeUser",
			roles:      roles(chat.RoleSubscriber, chat.RoleModerator),
			wantAuthor: "SomeUser",
		},
		{
			name:       "broadcaster moderator",
			author:     "Owner",
			roles:      roles(chat.RoleModerator, chat.RoleBroadcaster),
			wantAuthor: "Owner",
		},
		{
			name:       "vip og subscriber",
			author:     "Another",
			roles:      roles(chat.RoleSubscriber, chat.RoleOG, chat.RoleVIP),
			wantAuthor: "Another",
		},
		{
			name:       "long username exceeding cap",
			author:     "abcdefghijklmnopqrstuvwx",
			wantAuthor: "abcdefghijklmno…",
		},
		{
			name:        "long moderator identity exceeding cap",
			author:      "abcdefghijklmnopq",
			authorWidth: 40,
			roles:       roles(chat.RoleModerator),
			wantAuthor:  "abcdefghijklmnopq",
		},
		{
			name:       "raw and verified badges are not roles",
			author:     "user",
			wantAuthor: "user",
		},
		{
			name:        "unicode display width alignment with role",
			author:      "名",
			authorWidth: 10,
			roles:       roles(chat.RoleVIP),
			wantAuthor:  "名",
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
			got := strings.TrimSuffix(b.String(), "\n")
			plain := Sanitize(got)
			authorWidth := tt.authorWidth
			if authorWidth <= 0 {
				authorWidth = 16
			}
			authorWidth = min(authorWidth, maxIdentityWidth)
			wantPrefix := "[KICK]" + strings.Repeat(" ", providerColumnWidth-len("[KICK]")+columnGapWidth) +
				RenderRoleBadges(tt.roles) + strings.Repeat(" ", roleColumnWidth-runewidth.StringWidth(RenderRoleBadges(tt.roles))+columnGapWidth) +
				tt.wantAuthor + strings.Repeat(" ", authorWidth-runewidth.StringWidth(tt.wantAuthor)+columnGapWidth)
			if plain != wantPrefix+"hello" {
				t.Fatalf("Render() = %q, want prefix %q", got, wantPrefix)
			}
		})
	}
}

func TestProviderBadgeAndNicknameColumnsAlignAcrossPlatforms(t *testing.T) {
	tests := []struct {
		platform chat.Platform
		label    string
		roles    chat.RoleSet
		author   string
	}{
		{chat.PlatformKick, "[KICK]", 0, "A"},
		{chat.PlatformTwitch, "[Twitch]", roles(chat.RoleBroadcaster), "SleepyMario"},
		{chat.PlatformYouTube, "[YouTube]", roles(chat.RoleModerator, chat.RoleSubscriber), "abcdefghijklmnopqrstuvwx"},
		{chat.PlatformKick, "[KICK]", roles(chat.RoleVIP, chat.RoleOG, chat.RoleFollower), "名"},
		{chat.PlatformTwitch, "[Twitch]", roles(chat.RoleBroadcaster, chat.RoleModerator, chat.RolePartner, chat.RoleSubscriber), "viewer"},
	}
	wantBadgeColumn := providerColumnWidth + columnGapWidth
	wantAuthorColumn := wantBadgeColumn + roleColumnWidth + columnGapWidth
	wantMessageColumn := wantAuthorColumn + 16 + columnGapWidth
	for _, test := range tests {
		line := New(io.Discard, Options{}).Format(chat.Message{Platform: test.platform, AuthorDisplayName: test.author, Roles: test.roles, Text: "MESSAGE"})
		plain := Sanitize(line.Text)
		if !strings.HasPrefix(plain, test.label) {
			t.Fatalf("platform=%s line=%q", test.platform, plain)
		}
		if badges := RenderRoleBadges(test.roles); badges != "" && runewidth.StringWidth(plain[:strings.Index(plain, badges)]) != wantBadgeColumn {
			t.Fatalf("platform=%s badge column line=%q", test.platform, plain)
		}
		visibleAuthor := truncateDisplayWidth(test.author, 16)
		if runewidth.StringWidth(plain[:strings.Index(plain, visibleAuthor)]) != wantAuthorColumn {
			t.Fatalf("platform=%s author column line=%q", test.platform, plain)
		}
		if runewidth.StringWidth(plain[:strings.Index(plain, "MESSAGE")]) != wantMessageColumn {
			t.Fatalf("platform=%s message column line=%q", test.platform, plain)
		}
	}
}

func TestRenderRoleBadgesFixedOrderAndUnknownOmitted(t *testing.T) {
	got := RenderRoleBadges(roles(chat.RoleFollower, chat.RoleSubscriber, chat.RoleOG, chat.RoleVIP, chat.RolePartner, chat.RoleModerator, chat.RoleBroadcaster))
	if got != "[B][M][P][V]" {
		t.Fatalf("badges=%q", got)
	}
	available := []struct {
		role chat.Role
		want string
	}{
		{chat.RoleBroadcaster, "[B]"},
		{chat.RoleModerator, "[M]"},
		{chat.RolePartner, "[P]"},
		{chat.RoleVIP, "[V]"},
		{chat.RoleOG, "[O]"},
		{chat.RoleSubscriber, "[S]"},
		{chat.RoleFollower, "[F]"},
	}
	for _, badge := range available {
		if got = RenderRoleBadges(roles(badge.role)); got != badge.want {
			t.Fatalf("role=%v badge=%q want=%q", badge.role, got, badge.want)
		}
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
