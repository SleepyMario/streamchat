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

const (
	maxIdentityWidth    = 22
	providerColumnWidth = 9 // display width of "[YouTube]"
	roleSlotCapacity    = 4
	roleColumnWidth     = roleSlotCapacity * 3 // display width of four compact "[X]" badges
	columnGapWidth      = 2
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

type roleBadge struct {
	role   chat.Role
	marker string
}

var genericRoleBadges = []roleBadge{
	{chat.RoleBroadcaster, "[B]"},
	{chat.RoleModerator, "[M]"},
	{chat.RolePartner, "[P]"},
	{chat.RoleVIP, "[V]"},
	{chat.RoleOG, "[O]"},
	{chat.RoleSubscriber, "[S]"},
	{chat.RoleFollower, "[F]"},
}

// TwitchRoleBadges deliberately use stable Unicode symbols rather than remote
// badge artwork. The meanings track Twitch's familiar badge vocabulary while
// retaining a predictable, fixed-width terminal layout. Raw provider badge IDs
// remain available on chat.Message for graphical clients.
var twitchRoleBadges = []roleBadge{
	{chat.RoleBroadcaster, "🔴"},
	{chat.RoleModerator, "🗡️"},
	{chat.RolePartner, "✅"},
	{chat.RoleVIP, "💎"},
	{chat.RoleOG, "1️⃣"},
	{chat.RoleSubscriber, "⭐"},
	{chat.RoleFollower, "💜"},
}

// KickRoleBadges use the same stable terminal-safe approach as Twitch's
// markers, but follow Kick's green visual identity and its channel-role
// vocabulary. Provider artwork and custom subscriber images remain preserved
// in chat.Message.Badges for graphical clients.
var kickRoleBadges = []roleBadge{
	{chat.RoleBroadcaster, "🟢"},
	{chat.RoleModerator, "🛡️"},
	{chat.RolePartner, "✅"},
	{chat.RoleVIP, "💎"},
	{chat.RoleOG, "🏆"},
	{chat.RoleSubscriber, "⭐"},
	{chat.RoleFollower, "💚"},
}

func renderRoleBadges(roles chat.RoleSet, available []roleBadge) string {
	var badges strings.Builder
	rendered := 0
	for _, badge := range available {
		if roles.Has(badge.role) {
			badges.WriteString(badge.marker)
			rendered++
			if rendered == roleSlotCapacity {
				break
			}
		}
	}
	return badges.String()
}

func RenderRoleBadges(roles chat.RoleSet) string {
	return renderRoleBadges(roles, genericRoleBadges)
}

func RenderPlatformRoleBadges(platform chat.Platform, roles chat.RoleSet) string {
	switch platform {
	case chat.PlatformTwitch:
		return renderRoleBadges(roles, twitchRoleBadges)
	case chat.PlatformKick:
		return renderRoleBadges(roles, kickRoleBadges)
	default:
		return RenderRoleBadges(roles)
	}
}

func providerLabel(platform chat.Platform) string {
	switch platform {
	case chat.PlatformKick:
		return "[KICK]"
	case chat.PlatformTwitch:
		return "[Twitch]"
	case chat.PlatformYouTube:
		return "[YouTube]"
	default:
		return "[Unknown]"
	}
}

func truncateDisplayWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(value) <= width {
		return value
	}
	limit := width - 1
	var result strings.Builder
	used := 0
	for _, r := range value {
		runeWidth := runewidth.RuneWidth(r)
		if used+runeWidth > limit {
			break
		}
		result.WriteRune(r)
		used += runeWidth
	}
	result.WriteRune('…')
	return result.String()
}

func fieldPadding(value string, width int) string {
	return strings.Repeat(" ", max(width-runewidth.StringWidth(value), 0))
}

func (t *Terminal) Render(m chat.Message) error {
	line := t.Format(m)
	_, e := fmt.Fprintln(t.w, line.Text)
	return e
}

func (t *Terminal) Format(m chat.Message) emote.Line {
	p := ""
	if t.opt.Timestamps {
		p = m.Timestamp.Local().Format("15:04:05 ")
	}
	author := Sanitize(m.AuthorDisplayName)
	actualChatter := author != ""
	if author == "" {
		author = "system"
	}
	authorWidth := min(t.opt.AuthorWidth, maxIdentityWidth)
	author = truncateDisplayWidth(author, authorWidth)
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
	badges := RenderPlatformRoleBadges(m.Platform, m.Roles)
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
	pl := providerLabel(m.Platform)
	plainProvider := pl
	if t.opt.Color && !(chatterANSI != "" && t.opt.ChatColorMode == chattercolor.ModeLine) {
		c := "\x1b[31m"
		if m.Platform == chat.PlatformKick {
			c = "\x1b[32m"
		} else if m.Platform == chat.PlatformTwitch {
			c = "\x1b[35m"
		}
		pl = c + pl + "\x1b[0m"
	}
	prefix := p +
		pl + fieldPadding(plainProvider, providerColumnWidth) + strings.Repeat(" ", columnGapWidth) +
		badges + fieldPadding(badges, roleColumnWidth) + strings.Repeat(" ", columnGapWidth) +
		displayAuthor + fieldPadding(author, authorWidth) + strings.Repeat(" ", columnGapWidth) +
		textPrefix
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
