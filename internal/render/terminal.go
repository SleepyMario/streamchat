package render

import (
	"fmt"
	"github.com/SleepyMario/streamchat/internal/chat"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
)

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

func renderBadge(b chat.Badge) string {
	typeName := strings.ToLower(strings.TrimSpace(b.Type))
	text := strings.ToLower(strings.TrimSpace(b.Text))

	switch {
	case typeName == "moderator" || text == "moderator":
		return "[MOD]"
	case typeName == "broadcaster" || text == "broadcaster":
		return ""
	case typeName == "verified channel" || text == "verified channel":
		return ""
	}

	label := b.Text
	if label == "" {
		label = b.Type
	}
	if label == "" {
		return ""
	}
	return "[" + strings.ToUpper(Sanitize(label)) + "]"
}

func (t *Terminal) Render(m chat.Message) error {
	label := "YT"
	if m.Platform == chat.PlatformKick {
		label = "KICK"
	} else if m.Platform == chat.PlatformTwitch {
		label = "TW"
	}
	bs := []string{}
	for _, b := range m.Badges {
		if badge := renderBadge(b); badge != "" {
			bs = append(bs, badge)
		}
	}
	p := ""
	if t.opt.Timestamps {
		p = m.Timestamp.Local().Format("15:04:05 ")
	}
	author := Sanitize(m.AuthorDisplayName)
	if author == "" {
		author = "system"
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
	_, e := fmt.Fprintf(t.w, "%s%-6s %-*s %-14s %s\n", p, pl, t.opt.AuthorWidth, author, strings.Join(bs, ""), txt)
	return e
}
