package terminalui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
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
	if !strings.Contains(got, "[KICK] viewer  incoming\r\n") || !strings.Contains(got, "[KICK] > hello everyone") {
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
		if !strings.Contains(raw, "\r\x1b[2K\r"+message+"\r\n") {
			t.Fatalf("chat did not start at column one with CRLF termination: %q", raw)
		}
	}
	const coloredPrompt = "\r\x1b[36m[KICK]\x1b[39m \x1b[32m>\x1b[39m partially typed"
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
	if !strings.Contains(raw, "\x1b[999B\r\x1b[2K\r") {
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
	if !strings.Contains(raw, "\r\x1b[2K\r[KICK] viewer  世界\r\n") {
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

func TestScreenSerializesConcurrentInputAndMessages(t *testing.T) {
	var destination synchronizedBuffer
	screen := NewScreen(&destination, func() int { return 50 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	writer := screen.Writer(&destination)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, r := range "concurrent unicode 世界" {
			screen.Feed(r)
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
		})
	}
}

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
