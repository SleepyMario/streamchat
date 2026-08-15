package terminalui

import (
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

type Event struct {
	Line     string
	Submit   bool
	Shutdown bool
}

type Editor struct {
	text   []rune
	cursor int
	escape []rune
}

func (e *Editor) Text() string { return string(e.text) }
func (e *Editor) Cursor() int  { return e.cursor }

func (e *Editor) Feed(r rune) (Event, bool) {
	if len(e.escape) > 0 {
		return e.feedEscape(r)
	}
	switch r {
	case 0x1b:
		e.escape = []rune{r}
		return Event{}, false
	case '\r', '\n':
		line := string(e.text)
		e.text = nil
		e.cursor = 0
		return Event{Line: line, Submit: true}, true
	case 0x03:
		return Event{Shutdown: true}, false
	case 0x04:
		if len(e.text) == 0 {
			return Event{Shutdown: true}, false
		}
		if e.cursor < len(e.text) {
			e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
			return Event{}, true
		}
		return Event{}, false
	case 0x08, 0x7f:
		if e.cursor > 0 {
			e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
			e.cursor--
			return Event{}, true
		}
		return Event{}, false
	}
	if unicode.IsPrint(r) {
		e.text = append(e.text, 0)
		copy(e.text[e.cursor+1:], e.text[e.cursor:])
		e.text[e.cursor] = r
		e.cursor++
		return Event{}, true
	}
	return Event{}, false
}

func (e *Editor) feedEscape(r rune) (Event, bool) {
	e.escape = append(e.escape, r)
	if len(e.escape) == 2 {
		if r == '[' {
			return Event{}, false
		}
		if r == 'O' {
			return Event{}, false
		}
		e.resetEscape()
		return Event{}, false
	}
	if !escapeFinal(r) {
		if len(e.escape) > 16 {
			e.resetEscape()
		}
		return Event{}, false
	}
	sequence := string(e.escape)
	e.resetEscape()
	switch sequence {
	case "\x1b[D":
		if e.cursor > 0 {
			e.cursor--
			return Event{}, true
		}
	case "\x1b[C":
		if e.cursor < len(e.text) {
			e.cursor++
			return Event{}, true
		}
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
		if e.cursor != 0 {
			e.cursor = 0
			return Event{}, true
		}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
		if e.cursor != len(e.text) {
			e.cursor = len(e.text)
			return Event{}, true
		}
	}
	return Event{}, false
}

func (e *Editor) resetEscape() {
	e.escape = nil
}

func escapeFinal(r rune) bool { return r >= 0x40 && r <= 0x7e }

func (e *Editor) Window(width int) (string, int) {
	if width <= 0 {
		return "", 0
	}
	budget := max(width-1, 0)
	start := e.cursor
	used := 0
	for start > 0 {
		w := runewidth.RuneWidth(e.text[start-1])
		if used+w > budget {
			break
		}
		start--
		used += w
	}
	end := start
	visibleWidth := 0
	for end < len(e.text) {
		w := runewidth.RuneWidth(e.text[end])
		if visibleWidth+w > budget {
			break
		}
		visibleWidth += w
		end++
	}
	cursorWidth := runewidth.StringWidth(string(e.text[start:e.cursor]))
	return string(e.text[start:end]), cursorWidth
}

func TargetLabel(command string) string {
	switch strings.ToLower(command) {
	case "":
		return "NONE"
	case "kk", "kick":
		return "KICK"
	default:
		return strings.ToUpper(command)
	}
}
