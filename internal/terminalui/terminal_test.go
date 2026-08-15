package terminalui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestScreenIncomingOutputPreservesInputAndTarget(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output, func() int { return 40 })
	if err := screen.Start(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "[NONE] > ") {
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
	got := output.String()
	if !strings.Contains(got, "[KICK] viewer  incoming\n") || !strings.Contains(got, "[KICK] > hello everyone") {
		t.Fatalf("chat/input redraw missing: %q", got)
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
