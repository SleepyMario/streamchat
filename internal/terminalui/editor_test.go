package terminalui

import (
	"reflect"
	"testing"

	"github.com/SleepyMario/streamchat/internal/command"
)

func feed(t *testing.T, editor *Editor, input string) Event {
	t.Helper()
	var event Event
	for _, r := range input {
		event, _ = editor.Feed(r)
	}
	return event
}

func TestEditorInsertionBackspaceAndCursorMovement(t *testing.T) {
	var editor Editor
	feed(t, &editor, "helo")
	feed(t, &editor, "\x1b[D")
	feed(t, &editor, "l")
	feed(t, &editor, "\x1b[C")
	if editor.Text() != "hello" || editor.Cursor() != 5 {
		t.Fatalf("text=%q cursor=%d", editor.Text(), editor.Cursor())
	}
	feed(t, &editor, "\x1b[H")
	feed(t, &editor, "X")
	feed(t, &editor, "\x1b[F")
	feed(t, &editor, "Y")
	feed(t, &editor, "\x7f")
	if editor.Text() != "Xhello" || editor.Cursor() != 6 {
		t.Fatalf("text=%q cursor=%d", editor.Text(), editor.Cursor())
	}
}

func TestEditorInterpretsEscapeSequencesInsteadOfPrintingThem(t *testing.T) {
	var editor Editor
	feed(t, &editor, "abc")
	feed(t, &editor, "\x1b[D\x1b[D")
	feed(t, &editor, "X")
	feed(t, &editor, "\x1b[4~")
	feed(t, &editor, "Z")
	if editor.Text() != "aXbcZ" {
		t.Fatalf("text contains an escape sequence or wrong edit: %q", editor.Text())
	}
	for _, r := range editor.Text() {
		if r == 0x1b {
			t.Fatal("raw escape was inserted")
		}
	}
}

func TestEditorSubmitCtrlCAndCtrlD(t *testing.T) {
	var editor Editor
	feed(t, &editor, "hello")
	event, _ := editor.Feed('\r')
	if !event.Submit || event.Line != "hello" || editor.Text() != "" {
		t.Fatalf("event=%+v text=%q", event, editor.Text())
	}
	event, _ = editor.Feed(0x03)
	if !event.Shutdown {
		t.Fatal("Ctrl-C did not request shutdown")
	}
	event, _ = editor.Feed(0x04)
	if !event.Shutdown {
		t.Fatal("Ctrl-D on an empty editor did not request shutdown")
	}
}

func TestEditorUnicodeDisplayWidthWindow(t *testing.T) {
	var editor Editor
	feed(t, &editor, "你a🙂")
	text, cursor := editor.Window(5)
	if text != "a🙂" || cursor != 3 {
		t.Fatalf("window=%q cursorWidth=%d", text, cursor)
	}
}

func TestTargetLabel(t *testing.T) {
	if TargetLabel("") != "NONE" || TargetLabel("kick") != "KICK" || TargetLabel("twitch") != "Twitch" || TargetLabel("yt") != "YT" {
		t.Fatalf("labels: none=%q kick=%q twitch=%q youtube=%q", TargetLabel(""), TargetLabel("kick"), TargetLabel("twitch"), TargetLabel("yt"))
	}
}

func TestEditorSubmitsModerationCommandsUnchanged(t *testing.T) {
	for _, command := range []string{"/ban kick Viewer", "/timeout kick Viewer 10m"} {
		var editor Editor
		event := feed(t, &editor, command+"\r")
		if !event.Submit || event.Line != command {
			t.Fatalf("command=%q event=%+v", command, event)
		}
	}
}

func autocompleteEditor() Editor {
	return NewEditor(command.Streamchat())
}

func TestEditorTopLevelAutocompletePrefixes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{input: "/c", want: []string{"category", "clean", "clear"}},
		{input: "/cl", want: []string{"clean", "clear"}},
		{input: "/tw", want: []string{"twitch"}},
	} {
		editor := autocompleteEditor()
		feed(t, &editor, test.input)
		got, selected := editor.Suggestions()
		if !reflect.DeepEqual(got, test.want) || selected != -1 {
			t.Fatalf("input=%q suggestions=%v selected=%d", test.input, got, selected)
		}
	}
}

func TestEditorSingleCandidateCompletesAndEntersHierarchy(t *testing.T) {
	editor := autocompleteEditor()
	feed(t, &editor, "/b\t")
	if editor.Text() != "/ban " {
		t.Fatalf("text=%q", editor.Text())
	}
	want := []string{"kick", "youtube", "twitch"}
	if got, selected := editor.Suggestions(); !reflect.DeepEqual(got, want) || selected != -1 {
		t.Fatalf("suggestions=%v selected=%d", got, selected)
	}
}

func TestEditorHierarchicalSuggestions(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{input: "/clear ", want: []string{"kick", "youtube", "twitch"}},
		{input: "/open ", want: []string{"kick", "youtube", "twitch"}},
		{input: "/clean ", want: []string{"streamchat", "kick"}},
		{input: "/timeout y", want: []string{"youtube"}},
	} {
		editor := autocompleteEditor()
		feed(t, &editor, test.input)
		got, _ := editor.Suggestions()
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("input=%q suggestions=%v", test.input, got)
		}
	}
}

func TestEditorSuggestionNavigationAndAcceptance(t *testing.T) {
	editor := autocompleteEditor()
	feed(t, &editor, "/cl")
	feed(t, &editor, "\x1b[B")
	if _, selected := editor.Suggestions(); selected != 0 {
		t.Fatalf("down selected=%d", selected)
	}
	feed(t, &editor, "\x1b[B")
	if _, selected := editor.Suggestions(); selected != 1 {
		t.Fatalf("second down selected=%d", selected)
	}
	feed(t, &editor, "\x1b[A")
	if _, selected := editor.Suggestions(); selected != 0 {
		t.Fatalf("up selected=%d", selected)
	}
	feed(t, &editor, "\x1b[B")
	event, changed := editor.Feed('\r')
	if event.Submit || !changed || editor.Text() != "/clear " {
		t.Fatalf("event=%+v changed=%t text=%q", event, changed, editor.Text())
	}
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"kick", "youtube", "twitch"}) {
		t.Fatalf("hierarchy after acceptance=%v", got)
	}
}

func TestEditorEnterWithoutActiveSuggestionSubmitsNormally(t *testing.T) {
	editor := autocompleteEditor()
	event := feed(t, &editor, "/cl\r")
	if !event.Submit || event.Line != "/cl" || editor.Text() != "" {
		t.Fatalf("event=%+v text=%q", event, editor.Text())
	}
}

func TestEditorDismissTypingAndBackspaceRecompute(t *testing.T) {
	editor := autocompleteEditor()
	feed(t, &editor, "/cl")
	if !editor.DismissSuggestions() {
		t.Fatal("suggestions were not dismissed")
	}
	if got, _ := editor.Suggestions(); len(got) != 0 {
		t.Fatalf("dismissed suggestions=%v", got)
	}
	feed(t, &editor, "e")
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"clean", "clear"}) {
		t.Fatalf("typing suggestions=%v", got)
	}
	feed(t, &editor, "a")
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"clean", "clear"}) {
		t.Fatalf("filtered suggestions=%v", got)
	}
	feed(t, &editor, "n")
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"clean"}) {
		t.Fatalf("single suggestion=%v", got)
	}
	feed(t, &editor, "\x7f")
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"clean", "clear"}) {
		t.Fatalf("backspace suggestions=%v", got)
	}
}

func TestEditorBufferedEscapeDismissesWithoutSwallowingFollowingKey(t *testing.T) {
	editor := autocompleteEditor()
	feed(t, &editor, "/cl")
	feed(t, &editor, "\x1b\x7f")
	if editor.Text() != "/c" {
		t.Fatalf("backspace after Escape was swallowed: %q", editor.Text())
	}
	if got, _ := editor.Suggestions(); !reflect.DeepEqual(got, []string{"category", "clean", "clear"}) {
		t.Fatalf("backspace did not recompute suggestions: %v", got)
	}
}

func TestEditorDoesNotSuggestRetiredOrFreeFormTokens(t *testing.T) {
	for _, input := range []string{"/kk", "/title Some title", "/ban kick viewer", "/timeout kick viewer 10m"} {
		editor := autocompleteEditor()
		feed(t, &editor, input)
		if got, _ := editor.Suggestions(); len(got) != 0 {
			t.Fatalf("input=%q suggestions=%v", input, got)
		}
	}
}
