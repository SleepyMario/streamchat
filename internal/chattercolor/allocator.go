// Package chattercolor assigns terminal colors to chatters in first-seen order.
package chattercolor

import (
	"strings"
	"sync"

	"github.com/SleepyMario/streamchat/internal/chat"
)

const (
	ModeLine     = "line"
	ModeUsername = "username"
	ModeOff      = "off"
)

func ValidMode(mode string) bool {
	return mode == ModeLine || mode == ModeUsername || mode == ModeOff
}

// Color is one entry in Streamchat's deliberately contrasting chatter palette.
type Color struct {
	Name string
	Hex  string
	ANSI string
}

var palette = [...]Color{
	{Name: "Cyan", Hex: "#00D7D7", ANSI: "\x1b[38;5;44m"},
	{Name: "Orange", Hex: "#FF8700", ANSI: "\x1b[38;5;208m"},
	{Name: "Magenta", Hex: "#D75FD7", ANSI: "\x1b[38;5;170m"},
	{Name: "Lime", Hex: "#87D700", ANSI: "\x1b[38;5;112m"},
	{Name: "Blue", Hex: "#5F87FF", ANSI: "\x1b[38;5;69m"},
	{Name: "Yellow", Hex: "#FFD75F", ANSI: "\x1b[38;5;221m"},
	{Name: "Violet", Hex: "#875FFF", ANSI: "\x1b[38;5;99m"},
	{Name: "Teal", Hex: "#00AF87", ANSI: "\x1b[38;5;36m"},
	{Name: "Red", Hex: "#FF5F5F", ANSI: "\x1b[38;5;203m"},
	{Name: "Mint", Hex: "#5FFFAF", ANSI: "\x1b[38;5;85m"},
	{Name: "Pink", Hex: "#FF87AF", ANSI: "\x1b[38;5;211m"},
	{Name: "Sky", Hex: "#5FD7FF", ANSI: "\x1b[38;5;81m"},
}

// Palette returns a copy of the fixed assignment palette in first-seen order.
func Palette() []Color {
	return append([]Color(nil), palette[:]...)
}

// Allocator retains first-seen assignments for one running client session.
type Allocator struct {
	mu          sync.Mutex
	assignments map[string]int
	next        int
}

func NewAllocator() *Allocator {
	return &Allocator{assignments: make(map[string]int)}
}

// Assign returns the chatter's stable session color. Provider IDs take
// precedence; otherwise a case-insensitive platform-and-username key is used.
func (a *Allocator) Assign(platform chat.Platform, authorID, username string) (Color, bool) {
	key := identity(platform, authorID, username)
	if key == "" || a == nil {
		return Color{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if index, ok := a.assignments[key]; ok {
		return palette[index], true
	}
	index := a.next % len(palette)
	a.assignments[key] = index
	a.next++
	return palette[index], true
}

func identity(platform chat.Platform, authorID, username string) string {
	provider := strings.ToLower(strings.TrimSpace(string(platform)))
	if provider == "" {
		return ""
	}
	if id := strings.TrimSpace(authorID); id != "" {
		return provider + "\x00id:" + id
	}
	if name := strings.ToLower(strings.TrimSpace(username)); name != "" {
		return provider + "\x00name:" + name
	}
	return ""
}
