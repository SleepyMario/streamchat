package render

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/mattn/go-runewidth"
)

const maxIdentityWidth = 22

var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func Sanitize(s string) string {
	s = ansi.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

type Options struct {
	Timestamps, Color bool
	AuthorWidth       int
}
type Terminal struct {
	w   io.Writer
	opt Options
}

func New(w io.Writer, o Options) *Terminal {
	if o.AuthorWidth <= 0 {
		o.AuthorWidth = 16
	}
	return &Terminal{w, o}
}
func ColorEnabled(no bool) bool {
	if no || os.Getenv("NO_COLOR") != "" {
		return false
	}
	st, e := os.Stdout.Stat()
	return e == nil && (st.Mode()&os.ModeCharDevice) != 0
}

func RenderRoleBadges(roles chat.RoleSet) string {
	ordered := []struct {
		role   chat.Role
		letter string
	}{
		{chat.RoleBroadcaster, "B"},
		{chat.RoleModerator, "M"},
		{chat.RolePartner, "P"},
		{chat.RoleVIP, "V"},
		{chat.RoleOG, "O"},
		{chat.RoleSubscriber, "S"},
		{chat.RoleFollower, "F"},
	}
	var badges strings.Builder
	for _, value := range ordered {
		if roles.Has(value.role) {
			badges.WriteByte('[')
			badges.WriteString(value.letter)
			badges.WriteByte(']')
		}
	}
	return badges.String()
}

func (t *Terminal) Render(m chat.Message) error {
	label := "YT"
	if m.Platform == chat.PlatformKick {
		label = "KICK"
	} else if m.Platform == chat.PlatformTwitch {
		label = "TW"
	}
	p := ""
	if t.opt.Timestamps {
		p = m.Timestamp.Local().Format("15:04:05 ")
	}
	author := Sanitize(m.AuthorDisplayName)
	if author == "" {
		author = "system"
	}
	identity := author
	if badges := RenderRoleBadges(m.Roles); badges != "" {
		identity = badges + " " + author
	}
	txt := Sanitize(m.Text)
	if m.Reply != nil {
		txt = "↪ " + Sanitize(m.Reply.AuthorDisplayName) + ": " + txt
	}
	if m.Paid != nil {
		txt = "[PAID " + Sanitize(m.Paid.Display) + "] " + txt
	}
	if m.Membership != nil {
		txt = "[MEMBER] " + txt
	}
	pl := "[" + label + "]"
	if t.opt.Color {
		c := "\x1b[31m"
		if m.Platform == chat.PlatformKick {
			c = "\x1b[32m"
		} else if m.Platform == chat.PlatformTwitch {
			c = "\x1b[35m"
		}
		pl = c + pl + "\x1b[0m"
	}
	identityWidth := min(t.opt.AuthorWidth, maxIdentityWidth)
	padding := max(identityWidth-runewidth.StringWidth(identity), 0)
	_, e := fmt.Fprintf(t.w, "%s%-6s %s%s%s\n", p, pl, identity, strings.Repeat(" ", padding+2), txt)
	return e
}
