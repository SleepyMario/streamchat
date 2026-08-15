package terminalui

import "testing"

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
	if TargetLabel("") != "NONE" || TargetLabel("kk") != "KICK" || TargetLabel("yt") != "YT" {
		t.Fatalf("labels: none=%q kick=%q youtube=%q", TargetLabel(""), TargetLabel("kk"), TargetLabel("yt"))
	}
}

func TestEditorSubmitsModerationCommandsUnchanged(t *testing.T) {
	for _, command := range []string{"/ban Viewer", "/timeout Viewer 10m"} {
		var editor Editor
		event := feed(t, &editor, command+"\r")
		if !event.Submit || event.Line != command {
			t.Fatalf("command=%q event=%+v", command, event)
		}
	}
}
