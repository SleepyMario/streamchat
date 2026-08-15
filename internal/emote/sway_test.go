package emote

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCollectSwayOverlaysKeepsOwningOutputOrigin(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{
		{Type: "output", Rect: swayRect{X: 3840, Y: 0, Width: 1920, Height: 1200}, Nodes: []swayNode{
			{Type: "workspace", FloatingNodes: []swayNode{
				{AppID: "ueberzugpp_abcd", PID: 42, Rect: swayRect{X: 8983, Y: 636, Width: 36, Height: 36}},
			}},
		}},
	}}
	got := collectSwayOverlays(tree, 42)
	if len(got) != 1 || got[0].Origin.X != 3840 || got[0].Rect.X != 8983 {
		t.Fatalf("overlays=%+v", got)
	}
}

func TestCollectSwayOverlaysUsesWorkspaceOriginBelowTopBar(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{
		{Type: "output", Rect: swayRect{X: 0, Y: 0, Width: 3840, Height: 2160}, Nodes: []swayNode{
			{Type: "workspace", Rect: swayRect{X: 0, Y: 54, Width: 3840, Height: 2106}, FloatingNodes: []swayNode{
				{AppID: "ueberzugpp_bar", PID: 43, Rect: swayRect{X: 5143, Y: 798, Width: 36, Height: 36}},
			}},
		}},
	}}
	got := collectSwayOverlays(tree, 43)
	if len(got) != 1 || got[0].Origin.Y != 54 {
		t.Fatalf("overlays=%+v", got)
	}
}

func TestSwayCorrectionUsesAbsoluteDesktopCoordinates(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{
		{Type: "output", Rect: swayRect{X: 3840, Y: 0, Width: 1920, Height: 1200}, Nodes: []swayNode{
			{Type: "workspace", FloatingNodes: []swayNode{
				{AppID: "ueberzugpp_abcd", PID: 42, Rect: swayRect{X: 8983, Y: 636, Width: 36, Height: 36}},
			}},
		}},
	}}
	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	corrector := &swayCorrector{origin: swayRect{X: 0, Y: 0}, corrected: make(map[string]struct{}), identifiers: make(map[string]string)}
	corrector.run = func(_ time.Duration, arguments ...string) ([]byte, error) {
		if len(arguments) == 3 && arguments[1] == "get_tree" {
			return data, nil
		}
		if len(arguments) == 1 {
			commands = append(commands, arguments[0])
			return []byte(`[{"success":true}]`), nil
		}
		return nil, nil
	}
	if err = corrector.Correct(42, "message-0", 1, true); err != nil {
		t.Fatal(err)
	}
	want := `[app_id="ueberzugpp_abcd"] move absolute position 5143 636`
	if !containsCommand(commands, want) {
		t.Fatalf("commands=%q want=%q", commands, want)
	}
	if len(commands) != 3 || !strings.Contains(commands[0], "opacity set 0") || !strings.Contains(commands[2], "opacity set 1") {
		t.Fatalf("new overlay was not concealed through correction: %q", commands)
	}
}

func TestSwayCorrectionHandlesOverlayCreatedOnDifferentOutput(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{
		{Type: "output", Rect: swayRect{X: 0, Y: 0, Width: 3840, Height: 2160}, FloatingNodes: []swayNode{
			{AppID: "ueberzugpp_xyz", PID: 77, Rect: swayRect{X: 1303, Y: 636, Width: 36, Height: 36}},
		}},
	}}
	data, _ := json.Marshal(tree)
	var commands []string
	corrector := &swayCorrector{origin: swayRect{X: 3840, Y: 0}, corrected: make(map[string]struct{}), identifiers: make(map[string]string)}
	corrector.run = func(_ time.Duration, arguments ...string) ([]byte, error) {
		if len(arguments) == 3 {
			return data, nil
		}
		commands = append(commands, strings.Join(arguments, " "))
		return nil, nil
	}
	if err := corrector.Correct(77, "message-0", 1, true); err != nil {
		t.Fatal(err)
	}
	if !containsCommandFragment(commands, "move absolute position 5143 636") {
		t.Fatalf("commands=%q", commands)
	}
}

func TestSwayCorrectionWaitsForUeberzugWindowMoveToSettle(t *testing.T) {
	makeTree := func(x int) []byte {
		tree := swayNode{Nodes: []swayNode{
			{Type: "output", Rect: swayRect{X: 3840, Width: 1920, Height: 1200}, FloatingNodes: []swayNode{
				{AppID: "ueberzugpp_late", PID: 88, Rect: swayRect{X: x, Y: 636, Width: 36, Height: 36}},
			}},
		}}
		data, _ := json.Marshal(tree)
		return data
	}
	queries := 0
	var commands []string
	corrector := &swayCorrector{origin: swayRect{}, corrected: make(map[string]struct{}), identifiers: make(map[string]string)}
	corrector.run = func(_ time.Duration, arguments ...string) ([]byte, error) {
		if len(arguments) == 3 {
			queries++
			if queries == 1 {
				return makeTree(5722), nil
			}
			return makeTree(8983), nil
		}
		commands = append(commands, arguments[0])
		return nil, nil
	}
	if err := corrector.Correct(88, "message-0", 1, true); err != nil {
		t.Fatal(err)
	}
	if queries < 3 || !containsCommandFragment(commands, "move absolute position 5143 636") {
		t.Fatalf("queries=%d commands=%q", queries, commands)
	}
}

func TestSwayMoveRowsReusesExistingOverlaySurface(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{
		{Type: "output", Rect: swayRect{X: 3840, Width: 1920, Height: 1200}, FloatingNodes: []swayNode{
			{AppID: "ueberzugpp_stable", PID: 91, Rect: swayRect{X: 5143, Y: 780, Width: 36, Height: 36}},
		}},
	}}
	data, _ := json.Marshal(tree)
	var commands []string
	corrector := &swayCorrector{
		corrected:   map[string]struct{}{"ueberzugpp_stable": {}},
		identifiers: map[string]string{"message-0": "ueberzugpp_stable"},
	}
	corrector.run = func(_ time.Duration, arguments ...string) ([]byte, error) {
		if len(arguments) == 3 {
			return data, nil
		}
		commands = append(commands, arguments[0])
		return []byte(`[{"success":true}]`), nil
	}
	moved, err := corrector.MoveRows(91, "message-0", -1)
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	want := `[app_id="ueberzugpp_stable"] move absolute position 5143 744`
	if !containsCommand(commands, want) {
		t.Fatalf("commands=%q want=%q", commands, want)
	}
}

func containsCommand(commands []string, want string) bool {
	for _, command := range commands {
		if command == want {
			return true
		}
	}
	return false
}

func containsCommandFragment(commands []string, want string) bool {
	for _, command := range commands {
		if strings.Contains(command, want) {
			return true
		}
	}
	return false
}
