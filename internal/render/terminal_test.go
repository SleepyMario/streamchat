package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
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
