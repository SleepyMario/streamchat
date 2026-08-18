package terminalui

import (
	"strings"
	"unicode"

	"github.com/SleepyMario/streamchat/internal/command"
	"github.com/mattn/go-runewidth"
)

type Event struct {
	Line     string
	Submit   bool
	Shutdown bool
}

type Editor struct {
	text             []rune
	cursor           int
	escape           []rune
	registry         *command.Registry
	suggestions      []string
	selected         int
	replacementStart int
	replacementEnd   int
	completionPath   []string
}

func NewEditor(registry *command.Registry) Editor {
	return Editor{registry: registry, selected: -1}
}

func (e *Editor) Text() string { return string(e.text) }
func (e *Editor) Cursor() int  { return e.cursor }

func (e *Editor) Suggestions() ([]string, int) {
	return append([]string(nil), e.suggestions...), e.selected
}

func (e *Editor) DismissSuggestions() bool {
	e.resetEscape()
	if len(e.suggestions) == 0 && e.selected < 0 {
		return false
	}
	e.suggestions = nil
	e.selected = -1
	return true
}

func (e *Editor) Feed(r rune) (Event, bool) {
	if len(e.escape) > 0 {
		return e.feedEscape(r)
	}
	switch r {
	case 0x1b:
		e.escape = []rune{r}
		return Event{}, false
	case '\t':
		return Event{}, e.complete()
	case '\r', '\n':
		if e.selected >= 0 && e.selected < len(e.suggestions) {
			e.acceptSuggestion(e.selected)
			return Event{}, true
		}
		line := string(e.text)
		e.text = nil
		e.cursor = 0
		e.dismissCompletion()
		return Event{Line: line, Submit: true}, true
	case 0x03:
		return Event{Shutdown: true}, false
	case 0x04:
		if len(e.text) == 0 {
			return Event{Shutdown: true}, false
		}
		if e.cursor < len(e.text) {
			e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
			e.recomputeSuggestions()
			return Event{}, true
		}
		return Event{}, false
	case 0x08, 0x7f:
		if e.cursor > 0 {
			e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
			e.cursor--
			e.recomputeSuggestions()
			return Event{}, true
		}
		return Event{}, false
	}
	if unicode.IsPrint(r) {
		e.text = append(e.text, 0)
		copy(e.text[e.cursor+1:], e.text[e.cursor:])
		e.text[e.cursor] = r
		e.cursor++
		e.recomputeSuggestions()
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
		dismissed := e.DismissSuggestions()
		event, changed := e.Feed(r)
		return event, dismissed || changed
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
	case "\x1b[A":
		return Event{}, e.moveSuggestion(-1)
	case "\x1b[B":
		return Event{}, e.moveSuggestion(1)
	case "\x1b[D":
		if e.cursor > 0 {
			e.cursor--
			e.recomputeSuggestions()
			return Event{}, true
		}
	case "\x1b[C":
		if e.cursor < len(e.text) {
			e.cursor++
			e.recomputeSuggestions()
			return Event{}, true
		}
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
		if e.cursor != 0 {
			e.cursor = 0
			e.recomputeSuggestions()
			return Event{}, true
		}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
		if e.cursor != len(e.text) {
			e.cursor = len(e.text)
			e.recomputeSuggestions()
			return Event{}, true
		}
	}
	return Event{}, false
}

func (e *Editor) complete() bool {
	if len(e.suggestions) == 0 {
		e.recomputeSuggestions()
	}
	switch len(e.suggestions) {
	case 0:
		return false
	case 1:
		e.acceptSuggestion(0)
		return true
	default:
		return e.moveSuggestion(1)
	}
}

func (e *Editor) moveSuggestion(delta int) bool {
	if len(e.suggestions) == 0 {
		return false
	}
	if e.selected < 0 {
		if delta < 0 {
			e.selected = len(e.suggestions) - 1
		} else {
			e.selected = 0
		}
		return true
	}
	e.selected = (e.selected + delta + len(e.suggestions)) % len(e.suggestions)
	return true
}

func (e *Editor) acceptSuggestion(index int) {
	if index < 0 || index >= len(e.suggestions) {
		return
	}
	candidate := e.suggestions[index]
	replacement := []rune(candidate)
	updated := make([]rune, 0, len(e.text)-(e.replacementEnd-e.replacementStart)+len(replacement)+1)
	updated = append(updated, e.text[:e.replacementStart]...)
	updated = append(updated, replacement...)
	updated = append(updated, e.text[e.replacementEnd:]...)
	e.text = updated
	e.cursor = e.replacementStart + len(replacement)
	path := append(append([]string(nil), e.completionPath...), candidate)
	if e.registry != nil && e.registry.HasChildren(path) {
		if e.cursor < len(e.text) && unicode.IsSpace(e.text[e.cursor]) {
			e.cursor++
		} else {
			e.text = append(e.text, 0)
			copy(e.text[e.cursor+1:], e.text[e.cursor:])
			e.text[e.cursor] = ' '
			e.cursor++
		}
		e.recomputeSuggestions()
		return
	}
	e.dismissCompletion()
}

func (e *Editor) recomputeSuggestions() {
	e.dismissCompletion()
	if e.registry == nil || e.cursor == 0 || len(e.text) == 0 || e.text[0] != '/' {
		return
	}
	start := e.cursor
	for start > 0 && !unicode.IsSpace(e.text[start-1]) {
		start--
	}
	var prefix string
	var path []string
	if start == 0 {
		if e.cursor < 1 {
			return
		}
		start = 1
		prefix = string(e.text[start:e.cursor])
	} else {
		path = strings.Fields(string(e.text[1:start]))
		prefix = string(e.text[start:e.cursor])
	}
	end := e.cursor
	for end < len(e.text) && !unicode.IsSpace(e.text[end]) {
		end++
	}
	suggestions := e.registry.Suggest(path, prefix)
	if len(suggestions) == 0 {
		return
	}
	e.suggestions = suggestions
	e.replacementStart = start
	e.replacementEnd = end
	e.completionPath = append([]string(nil), path...)
}

func (e *Editor) dismissCompletion() {
	e.suggestions = nil
	e.selected = -1
	e.replacementStart = 0
	e.replacementEnd = 0
	e.completionPath = nil
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
	case "kick":
		return "KICK"
	case "twitch":
		return "TW"
	default:
		return strings.ToUpper(command)
	}
}
