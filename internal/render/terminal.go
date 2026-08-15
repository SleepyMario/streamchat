package render

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/chattercolor"
	"github.com/SleepyMario/streamchat/internal/emote"
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
	Timestamps, Color  bool
	AuthorWidth        int
	Emotes             emote.Resolver
	ChatColorMode      string
	ChatterColorSource *chattercolor.Allocator
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
	line := t.Format(m)
	_, e := fmt.Fprintln(t.w, line.Text)
	return e
}

func (t *Terminal) Format(m chat.Message) emote.Line {
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
	actualChatter := author != ""
	if author == "" {
		author = "system"
	}
	chatterANSI := ""
	if actualChatter && t.opt.Color && t.opt.ChatColorMode != chattercolor.ModeOff && t.opt.ChatterColorSource != nil {
		if assigned, ok := t.opt.ChatterColorSource.Assign(m.Platform, m.AuthorID, m.AuthorDisplayName); ok {
			chatterANSI = assigned.ANSI
		}
	}
	displayAuthor := author
	if chatterANSI != "" && t.opt.ChatColorMode == chattercolor.ModeUsername {
		displayAuthor = chatterANSI + author + "\x1b[0m"
	}
	identity := displayAuthor
	if badges := RenderRoleBadges(m.Roles); badges != "" {
		identity = badges + " " + displayAuthor
	}
	body := emote.FormatText(m.Platform, m.Text, m.Emotes, Sanitize, t.opt.Emotes)
	textPrefix := ""
	if m.Reply != nil {
		textPrefix += "↪ " + Sanitize(m.Reply.AuthorDisplayName) + ": "
	}
	if m.Paid != nil {
		textPrefix += "[PAID " + Sanitize(m.Paid.Display) + "] "
	}
	if m.Membership != nil {
		textPrefix += "[MEMBER] "
	}
	pl := "[" + label + "]"
	if t.opt.Color && !(chatterANSI != "" && t.opt.ChatColorMode == chattercolor.ModeLine) {
		c := "\x1b[31m"
		if m.Platform == chat.PlatformKick {
			c = "\x1b[32m"
		} else if m.Platform == chat.PlatformTwitch {
			c = "\x1b[35m"
		}
		pl = c + pl + "\x1b[0m"
	}
	identityWidth := min(t.opt.AuthorWidth, maxIdentityWidth)
	padding := max(identityWidth-runewidth.StringWidth(Sanitize(identity)), 0)
	prefix := fmt.Sprintf("%s%-6s %s%s%s", p, pl, identity, strings.Repeat(" ", padding+2), textPrefix)
	line := emote.Line{Text: prefix + body.Text, Images: append([]emote.InlineImage(nil), body.Images...)}
	if body.GraphicalText != "" {
		line.GraphicalText = prefix + body.GraphicalText
	}
	offset := runewidth.StringWidth(Sanitize(prefix))
	for index := range line.Images {
		line.Images[index].Column += offset
	}
	if chatterANSI != "" && t.opt.ChatColorMode == chattercolor.ModeLine {
		line.Text = chatterANSI + line.Text + "\x1b[0m"
		if line.GraphicalText != "" {
			line.GraphicalText = chatterANSI + line.GraphicalText + "\x1b[0m"
		}
	} else if chatterANSI != "" && t.opt.ChatColorMode == chattercolor.ModeUsername {
		line.Text += "\x1b[0m"
		if line.GraphicalText != "" {
			line.GraphicalText += "\x1b[0m"
		}
	}
	return line
}
