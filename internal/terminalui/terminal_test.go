package terminalui

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/mattn/go-runewidth"
)

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func plainTerminalOutput(value string) string {
	return ansiSequence.ReplaceAllString(value, "")
}

func TestScreenIncomingOutputPreservesInputAndTarget(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plainTerminalOutput(output.String()), "[NONE] > ") {
		t.Fatalf("missing initial target: %q", output.String())
	}
	for _, r := range "hello everyone" {
		screen.Feed(r)
	}
	screen.SetTarget("kk")
	writer := screen.Writer(&output)
	if _, err := io.WriteString(writer, "[KICK] viewer  incoming\n"); err != nil {
		t.Fatal(err)
	}
	if screen.Text() != "hello everyone" {
		t.Fatalf("input was lost: %q", screen.Text())
	}
	got := plainTerminalOutput(output.String())
	if !strings.Contains(got, "[KICK] viewer  incoming") || !strings.Contains(got, "[KICK] > hello everyone") {
		t.Fatalf("chat/input redraw missing: %q", got)
	}
}

func TestScreenConsecutiveMessagesStartAtColumnOneWithoutDecoration(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 48 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kk")
	for _, r := range "partially typed" {
		screen.Feed(r)
	}
	output.Reset()

	writer := screen.Writer(&output)
	for _, message := range []string{"first chat\n", "second is longer\n", "third\n"} {
		if _, err := io.WriteString(writer, message); err != nil {
			t.Fatal(err)
		}
	}

	raw := output.String()
	if strings.Contains(raw, "─") {
		t.Fatalf("separator leaked into output: %q", raw)
	}
	for _, message := range []string{"first chat", "second is longer", "third"} {
		if !strings.Contains(raw, "\x1bD\r\x1b[2K"+message) {
			t.Fatalf("chat did not start at column one with CRLF termination: %q", raw)
		}
	}
	const coloredPrompt = "\x1b[24;1H\x1b[2K\x1b[36m[KICK]\x1b[39m \x1b[32m>\x1b[39m partially typed"
	if count := strings.Count(raw, coloredPrompt); count != 3 {
		t.Fatalf("prompt redraw count=%d; output=%q", count, raw)
	}
	if screen.Text() != "partially typed" {
		t.Fatalf("partial input was lost: %q", screen.Text())
	}
}

func TestScreenStartsAtColumnOneAndUsesDefaultNamedColors(t *testing.T) {
	var output bytes.Buffer
	if _, err := io.WriteString(&output, "cursor is not at column one"); err != nil {
		t.Fatal(err)
	}
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kk")
	raw := output.String()
	if !strings.Contains(raw, "cursor is not at column one"+enterAlternateScreen) {
		t.Fatalf("alternate screen was not entered first: %q", raw)
	}
	if !strings.Contains(raw, "\x1b[2J\x1b[H") {
		t.Fatalf("screen did not reset to column one: %q", raw)
	}
	if !strings.Contains(raw, "\x1b[36m[KICK]\x1b[39m \x1b[32m>\x1b[39m ") {
		t.Fatalf("named-color prompt missing: %q", raw)
	}
	if strings.Contains(raw, "38;2") || strings.Contains(raw, "38;5") {
		t.Fatalf("prompt used a hard-coded RGB/indexed color: %q", raw)
	}
}

func TestScreenResizeThenIncomingUnicodePreservesInput(t *testing.T) {
	var output bytes.Buffer
	width := 24
	screen := NewScreen(&output, func() int { return width })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kk")
	for _, r := range "你好🙂 typing" {
		screen.Feed(r)
	}
	width = 16
	screen.Redraw()
	output.Reset()
	if _, err := io.WriteString(screen.Writer(&output), "[KICK] viewer  世界\n"); err != nil {
		t.Fatal(err)
	}
	if screen.Text() != "你好🙂 typing" {
		t.Fatalf("Unicode input changed after resize/redraw: %q", screen.Text())
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1bD\r\x1b[2K[KICK] viewer  世界") {
		t.Fatalf("incoming message drifted after resize: %q", raw)
	}
	if strings.Contains(raw, "─") {
		t.Fatalf("separator fragment appeared after resize: %q", raw)
	}
}

func TestScreenLongUnicodeInputStaysWithinDisplayWindow(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 18 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "前半部分🙂tail" {
		screen.Feed(r)
	}
	if screen.Text() != "前半部分🙂tail" {
		t.Fatalf("input was truncated: %q", screen.Text())
	}
	if !strings.Contains(output.String(), "tail") {
		t.Fatalf("cursor end was not kept visible: %q", output.String())
	}
}

func TestScreenRendersThreeFixedStatusLinesAndLiveOfflineViewers(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	for row, want := range []string{"Title:    unavailable", "Category: unavailable", "Viewers:  unavailable"} {
		if !strings.Contains(plainTerminalOutput(raw), want) || !strings.Contains(raw, fmt.Sprintf("\x1b[%d;1H", row+1)) {
			t.Fatalf("missing status row %d %q: %q", row+1, want, raw)
		}
	}
	if !strings.Contains(raw, "\x1b[5;8r") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("fixed chat/input layout missing: %q", raw)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Live title", Category: "Just Chatting", ViewerCount: 42, Live: true})
	plain := plainTerminalOutput(output.String())
	for _, want := range []string{"Title:    Live title", "Category: Just Chatting", "Viewers:  42"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing live status %q: %q", want, plain)
		}
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Offline title", Category: "Games", ViewerCount: 99, Live: false})
	if plain = plainTerminalOutput(output.String()); !strings.Contains(plain, "Viewers:  OFFLINE") || strings.Contains(plain, "Viewers:  99") {
		t.Fatalf("offline viewers=%q", plain)
	}
}

func TestScreenRendersFixedWidthSeparatorsAndResizesThem(t *testing.T) {
	var output bytes.Buffer
	width, height := 12, 10
	screen := newScreen(&output, func() int { return width }, func() int { return height })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\x1b[4;1H\x1b[2K------------",
		"\x1b[9;1H\x1b[2K------------",
		"\x1b[5;8r",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing fixed layout %q: %q", want, output.String())
		}
	}

	width, height = 7, 8
	output.Reset()
	screen.Redraw()
	raw := output.String()
	for _, want := range []string{
		"\x1b[4;1H\x1b[2K-------",
		"\x1b[7;1H\x1b[2K-------",
		"\x1b[5;6r",
		"\x1b[8;1H",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("resized layout missing %q: %q", want, raw)
		}
	}
	if strings.Contains(raw, "------------") {
		t.Fatalf("separator retained old width: %q", raw)
	}
}

func TestScreenStatusTruncatesLongUnicodeByDisplayWidth(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 18 }, func() int { return 8 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "世界🙂 very long title", Category: "超長分類🙂tail", Live: true, ViewerCount: 7})
	raw := output.String()
	for row := 1; row <= 2; row++ {
		startMarker := fmt.Sprintf("\x1b[%d;1H", row)
		endMarker := fmt.Sprintf("\x1b[%d;1H", row+1)
		start := strings.Index(raw, startMarker)
		end := strings.Index(raw, endMarker)
		if start < 0 || end <= start {
			t.Fatalf("status row markers missing: %q", raw)
		}
		line := plainTerminalOutput(raw[start+len(startMarker) : end])
		if width := runewidth.StringWidth(line); width > 18 {
			t.Fatalf("row %d width=%d line=%q", row, width, line)
		}
	}
}

func TestIncomingChatUsesOnlyMiddleScrollRegion(t *testing.T) {
	var output bytes.Buffer
	screen := newScreen(&output, func() int { return 40 }, func() int { return 10 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetStatus(StreamStatus{Title: "Fixed", Category: "Games", Live: true, ViewerCount: 3})
	output.Reset()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Line: "[KICK] Alice  hello"}); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[8;1H\x1bD\r\x1b[2K[KICK] Alice  hello") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("chat/input rows incorrect: %q", raw)
	}
	for _, fixedRow := range []string{"\x1b[1;1H", "\x1b[2;1H", "\x1b[3;1H", "\x1b[4;1H", "\x1b[9;1H"} {
		if strings.Contains(raw, fixedRow) {
			t.Fatalf("incoming chat touched fixed row %q: %q", fixedRow, raw)
		}
	}
}

func TestStatusRedrawAndCleanPreserveInputCursorAndFixedRows(t *testing.T) {
	var output bytes.Buffer
	height := 10
	screen := newScreen(&output, func() int { return 32 }, func() int { return height })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, r := range "partly 你好🙂" {
		screen.Feed(r)
	}
	for _, r := range "\x1b[D\x1b[D" {
		screen.Feed(r)
	}
	cursor := screen.editor.Cursor()
	if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Author: "Alice", Line: "chat one"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	screen.SetStatus(StreamStatus{Title: "Updated", Category: "Games", Live: true, ViewerCount: 5})
	if screen.Text() != "partly 你好🙂" || screen.editor.Cursor() != cursor {
		t.Fatalf("status redraw changed editor text=%q cursor=%d", screen.Text(), screen.editor.Cursor())
	}
	if !strings.Contains(output.String(), "\x1b[10;1H") || !strings.Contains(plainTerminalOutput(output.String()), "[NONE] > partly 你好🙂") {
		t.Fatalf("input was not restored: %q", output.String())
	}
	output.Reset()
	if _, err := screen.CleanAll(); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	for _, fixedRow := range []string{"\x1b[1;1H", "\x1b[2;1H", "\x1b[3;1H", "\x1b[4;1H", "\x1b[9;1H"} {
		if strings.Contains(raw, fixedRow) {
			t.Fatalf("clean touched fixed row %q: %q", fixedRow, raw)
		}
	}
	if screen.status.Title != "Updated" || !strings.Contains(raw, "\x1b[5;1H\x1b[2K") || !strings.Contains(raw, "\x1b[10;1H") {
		t.Fatalf("clean did not preserve fixed state or redraw chat/input: status=%+v output=%q", screen.status, raw)
	}
	height = 8
	output.Reset()
	screen.Redraw()
	if raw := output.String(); !strings.Contains(raw, "\x1b[5;6r") || !strings.Contains(raw, "\x1b[4;1H") || !strings.Contains(raw, "\x1b[7;1H") || !strings.Contains(raw, "\x1b[8;1H") {
		t.Fatalf("resize layout incorrect: %q", raw)
	}
}

func TestScreenVerySmallHeightDegradesWithoutInvalidRegion(t *testing.T) {
	for _, height := range []int{1, 2, 3, 4, 5, 6, 7} {
		t.Run(fmt.Sprintf("height_%d", height), func(t *testing.T) {
			var output bytes.Buffer
			screen := newScreen(&output, func() int { return 12 }, func() int { return height })
			if err := screen.Start(); err != nil {
				t.Fatal(err)
			}
			if err := screen.AppendMessage(DisplayMessage{ID: "1", Platform: "kick", Line: "small chat"}); err != nil {
				t.Fatal(err)
			}
			raw := output.String()
			if strings.Contains(raw, "\x1b[5;5r") || strings.Contains(raw, "\x1b[5;4r") {
				t.Fatalf("invalid scroll region emitted: %q", raw)
			}
			if height == 6 && (!strings.Contains(raw, "\x1b[4;1H\x1b[2K------------") || !strings.Contains(raw, "\x1b[5;1H\x1b[2K------------")) {
				t.Fatalf("distinct separators missing at minimum fixed-layout height: %q", raw)
			}
			if height == 7 && !strings.Contains(raw, "\x1b[5;1H\x1b[2Ksmall chat") {
				t.Fatalf("single-row chat did not degrade safely: %q", raw)
			}
		})
	}
}

func TestScreenSerializesConcurrentInputAndMessages(t *testing.T) {
	var destination synchronizedBuffer
	screen := NewScreen(&destination, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	writer := screen.Writer(&destination)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for _, r := range "concurrent unicode 世界" {
			screen.Feed(r)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			screen.SetStatus(StreamStatus{Title: fmt.Sprintf("title %d", i), Category: "Games", Live: true, ViewerCount: i})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = fmt.Fprintf(writer, "message %d\n", i)
		}
	}()
	wg.Wait()
	if screen.Text() != "concurrent unicode 世界" || !strings.Contains(destination.String(), "message 19") {
		t.Fatalf("text=%q output=%q", screen.Text(), destination.String())
	}
}

func TestScreenCleanAllPreservesPartialUnicodeInput(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	screen.SetTarget("kk")
	for _, r := range "partly 你好🙂" {
		screen.Feed(r)
	}
	for _, r := range "\x1b[D\x1b[D" {
		screen.Feed(r)
	}
	cursorBefore := screen.editor.Cursor()
	for _, message := range []DisplayMessage{
		{Platform: "kick", Author: "Alice", Line: "[KICK] Alice  one\n"},
		{Platform: "youtube", Author: "Bob", Line: "[YT] Bob  two\n"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	removed, err := screen.CleanAll()
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if screen.Text() != "partly 你好🙂" {
		t.Fatalf("input changed: %q", screen.Text())
	}
	if screen.editor.Cursor() != cursorBefore {
		t.Fatalf("cursor changed: got=%d want=%d", screen.editor.Cursor(), cursorBefore)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[5;1H\x1b[2K") || !strings.Contains(plainTerminalOutput(raw), "[KICK] > partly 你好🙂") {
		t.Fatalf("view/input redraw missing: %q", raw)
	}
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b[4;1H") || strings.Contains(raw, "\x1b[23;1H") {
		t.Fatalf("clean touched status or separator layout: %q", raw)
	}
	if strings.Contains(raw, "Alice") || strings.Contains(raw, "Bob") {
		t.Fatalf("cleaned messages were replayed: %q", raw)
	}
	if strings.Contains(raw, leaveAlternateScreen) {
		t.Fatalf("clean unexpectedly left the alternate screen: %q", raw)
	}
}

func TestScreenCleanPlatformAndCaseInsensitiveAuthor(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []DisplayMessage{
		{Platform: "kick", Author: "BotRix", Line: "kick bot\n"},
		{Platform: "youtube", Author: "BotRix", Line: "youtube bot\n"},
		{Platform: "kick", Author: "Viewer", Line: "kick viewer\n"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	removed, err := screen.CleanPlatform("KICK")
	if err != nil || removed != 2 || len(screen.messages) != 1 || screen.messages[0].Platform != "youtube" {
		t.Fatalf("platform clean removed=%d messages=%v err=%v", removed, screen.messages, err)
	}
	if !strings.Contains(output.String(), "youtube bot") || strings.Contains(output.String(), "kick bot") || strings.Contains(output.String(), "kick viewer") {
		t.Fatalf("platform redraw=%q", output.String())
	}
	if strings.Contains(output.String(), "\x1b[4;1H") || strings.Contains(output.String(), "\x1b[23;1H") {
		t.Fatalf("platform clean touched separators: %q", output.String())
	}
	output.Reset()
	removed, err = screen.CleanAuthor("bOtRiX")
	if err != nil || removed != 1 || len(screen.messages) != 0 {
		t.Fatalf("author clean removed=%d messages=%v err=%v", removed, screen.messages, err)
	}
	if strings.Contains(output.String(), "\x1b[4;1H") || strings.Contains(output.String(), "\x1b[23;1H") {
		t.Fatalf("author clean touched separators: %q", output.String())
	}
}

func TestScreenCleanNoMatchDoesNotRedrawAndIncomingContinues(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if err := screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "Alice", Line: "old message\n"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	removed, err := screen.CleanAuthor("missing")
	if err != nil || removed != 0 || output.Len() != 0 {
		t.Fatalf("removed=%d output=%q err=%v", removed, output.String(), err)
	}
	if _, err = screen.CleanAll(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err = screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "New", Line: "new after clean\n"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "new after clean") || len(screen.messages) != 1 {
		t.Fatalf("incoming message did not continue: %q messages=%v", output.String(), screen.messages)
	}
}

func TestScreenDisplayBufferIsBounded(t *testing.T) {
	screen := NewScreen(io.Discard, func() int { return 40 })
	for i := 0; i < displayMessageLimit+25; i++ {
		if err := screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "user", Line: fmt.Sprintf("message %d\n", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(screen.messages) != displayMessageLimit || screen.messages[0].Line != "message 25\n" {
		t.Fatalf("buffer len=%d first=%q", len(screen.messages), screen.messages[0].Line)
	}
}

func TestScreenKnownMessageIDsFiltersAndDeduplicatesProviderIDs(t *testing.T) {
	screen := NewScreen(io.Discard, func() int { return 40 })
	for _, message := range []DisplayMessage{
		{ID: "kick-1", Platform: "kick"},
		{ID: "youtube-1", Platform: "youtube"},
		{ID: "kick-1", Platform: "KICK"},
		{ID: "", Platform: "kick"},
		{ID: "kick-2", Platform: "kick"},
	} {
		if err := screen.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if got := screen.KnownMessageIDs("kick"); !reflect.DeepEqual(got, []string{"kick-1", "kick-2"}) {
		t.Fatalf("IDs=%v", got)
	}
	if _, err := screen.CleanPlatform("kick"); err != nil {
		t.Fatal(err)
	}
	if got := screen.KnownMessageIDs("kick"); len(got) != 0 {
		t.Fatalf("cleaned IDs remain: %v", got)
	}
}

func TestScreenConcurrentIncomingAndCleanIsRaceSafe(t *testing.T) {
	var destination synchronizedBuffer
	screen := NewScreen(&destination, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = screen.AppendMessage(DisplayMessage{Platform: "kick", Author: "viewer", Line: fmt.Sprintf("incoming %d\n", i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = screen.CleanPlatform("kick")
		}
	}()
	wg.Wait()
	if len(screen.messages) > displayMessageLimit {
		t.Fatalf("buffer exceeded limit: %d", len(screen.messages))
	}
}

func TestTerminalCloseRestoresOnce(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	restored := 0
	terminal := &Terminal{
		screen:  screen,
		restore: func() error { restored++; return nil },
		stop:    make(chan struct{}),
		resize:  make(chan os.Signal, 1),
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restore calls=%d", restored)
	}
	if count := strings.Count(output.String(), leaveAlternateScreen); count != 1 {
		t.Fatalf("alternate-screen leave count=%d output=%q", count, output.String())
	}
}

func TestScreenStartupErrorLeavesAlternateScreenAndRestoresTerminal(t *testing.T) {
	output := &failNthWriter{failAt: 2}
	screen := NewScreen(output, func() int { return 40 })
	restored := 0
	err := startScreen(screen, func() error {
		restored++
		return nil
	})
	if err == nil {
		t.Fatal("expected startup failure")
	}
	if restored != 1 {
		t.Fatalf("terminal restore calls=%d", restored)
	}
	if !strings.Contains(output.String(), enterAlternateScreen) || !strings.Contains(output.String(), leaveAlternateScreen) {
		t.Fatalf("startup cleanup sequences missing: %q", output.String())
	}
}

func TestTerminalCloseStopsResizeWatcher(t *testing.T) {
	var output bytes.Buffer
	terminal := &Terminal{
		screen:  NewScreen(&output, func() int { return 40 }),
		restore: func() error { return nil },
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		resize:  make(chan os.Signal, 1),
	}
	go terminal.watchResize()
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminal.done:
	default:
		t.Fatal("resize watcher was still running after Close")
	}
}

func TestTerminalExitQuitAndCtrlCCleanup(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLine string
	}{
		{name: "exit", input: "/exit\r", wantLine: "/exit"},
		{name: "quit", input: "/quit\r", wantLine: "/quit"},
		{name: "ctrl-c", input: "\x03"},
		{name: "ctrl-d", input: "\x04"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			screen := NewScreen(&output, func() int { return 40 })
			if err := screen.Start(); err != nil {
				t.Fatal(err)
			}
			restored := false
			terminal := &Terminal{
				reader:  bufio.NewReader(strings.NewReader(test.input)),
				screen:  screen,
				restore: func() error { restored = true; return nil },
				stop:    make(chan struct{}),
				resize:  make(chan os.Signal, 1),
			}
			var event Event
			for !event.Submit && !event.Shutdown {
				var err error
				event, err = terminal.Next()
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.wantLine != "" && (!event.Submit || event.Line != test.wantLine) {
				t.Fatalf("event=%+v", event)
			}
			if test.wantLine == "" && !event.Shutdown {
				t.Fatalf("event=%+v", event)
			}
			if err := terminal.Close(); err != nil {
				t.Fatal(err)
			}
			if !restored {
				t.Fatal("terminal state was not restored")
			}
			if !strings.Contains(output.String(), leaveAlternateScreen) {
				t.Fatalf("main screen was not restored: %q", output.String())
			}
		})
	}
}

type failNthWriter struct {
	writes int
	failAt int
	b      bytes.Buffer
}

func (w *failNthWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("injected write failure")
	}
	return w.b.Write(p)
}

func (w *failNthWriter) String() string { return w.b.String() }

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
