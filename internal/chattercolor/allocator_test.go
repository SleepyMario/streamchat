package chattercolor

import (
	"reflect"
	"testing"

	"github.com/SleepyMario/streamchat/internal/chat"
)

func TestPaletteExactOrder(t *testing.T) {
	got := Palette()
	wantNames := []string{"Cyan", "Orange", "Magenta", "Lime", "Blue", "Yellow", "Violet", "Teal", "Red", "Mint", "Pink", "Sky"}
	wantHex := []string{"#00D7D7", "#FF8700", "#D75FD7", "#87D700", "#5F87FF", "#FFD75F", "#875FFF", "#00AF87", "#FF5F5F", "#5FFFAF", "#FF87AF", "#5FD7FF"}
	wantANSI := []string{"\x1b[38;5;44m", "\x1b[38;5;208m", "\x1b[38;5;170m", "\x1b[38;5;112m", "\x1b[38;5;69m", "\x1b[38;5;221m", "\x1b[38;5;99m", "\x1b[38;5;36m", "\x1b[38;5;203m", "\x1b[38;5;85m", "\x1b[38;5;211m", "\x1b[38;5;81m"}
	if len(got) != 12 {
		t.Fatalf("palette length=%d", len(got))
	}
	for index, color := range got {
		if color.Name != wantNames[index] || color.Hex != wantHex[index] || color.ANSI != wantANSI[index] {
			t.Fatalf("palette[%d]=%+v", index, color)
		}
	}
}

func TestAllocatorUsesFirstSeenOrderWrapsAndRetainsAssignments(t *testing.T) {
	allocator := NewAllocator()
	var assigned []string
	for index := 1; index <= 13; index++ {
		color, ok := allocator.Assign(chat.PlatformKick, string(rune('a'+index)), "viewer")
		if !ok {
			t.Fatalf("assignment %d unavailable", index)
		}
		assigned = append(assigned, color.Name)
	}
	if got, want := assigned[:3], []string{"Cyan", "Orange", "Magenta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first colors=%v", got)
	}
	if assigned[11] != "Sky" || assigned[12] != "Cyan" {
		t.Fatalf("wrap colors=%v", assigned[11:])
	}
	repeated, _ := allocator.Assign(chat.PlatformKick, "b", "renamed")
	if repeated.Name != "Cyan" {
		t.Fatalf("provider ID assignment changed: %s", repeated.Name)
	}
}

func TestAllocatorUsernameFallbackIsCaseInsensitiveAndPlatformScoped(t *testing.T) {
	allocator := NewAllocator()
	first, _ := allocator.Assign(chat.PlatformKick, "", " Viewer ")
	repeated, _ := allocator.Assign(chat.PlatformKick, "", "viewer")
	otherPlatform, _ := allocator.Assign(chat.PlatformTwitch, "", "viewer")
	if first.Name != "Cyan" || repeated != first || otherPlatform.Name != "Orange" {
		t.Fatalf("first=%+v repeated=%+v other=%+v", first, repeated, otherPlatform)
	}
}

func TestNewSessionStartsFromCyan(t *testing.T) {
	first, _ := NewAllocator().Assign(chat.PlatformKick, "1", "one")
	secondSession, _ := NewAllocator().Assign(chat.PlatformKick, "2", "two")
	if first.Name != "Cyan" || secondSession.Name != "Cyan" {
		t.Fatalf("first=%s second session=%s", first.Name, secondSession.Name)
	}
}

func TestAllocatorRejectsMessagesWithoutChatterIdentity(t *testing.T) {
	if _, ok := NewAllocator().Assign(chat.PlatformKick, "", ""); ok {
		t.Fatal("system message received a chatter color")
	}
}
