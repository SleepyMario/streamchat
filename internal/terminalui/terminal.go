package terminalui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const defaultWidth = 80

type Screen struct {
	mu      sync.Mutex
	out     io.Writer
	width   func() int
	editor  Editor
	target  string
	visible bool
}

func NewScreen(out io.Writer, width func() int) *Screen {
	return &Screen{out: out, width: width, target: "NONE"}
}

func (s *Screen) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.visible {
		return nil
	}
	s.visible = true
	if _, err := io.WriteString(s.out, "\x1b[999B\r\x1b[2K\r"); err != nil {
		return err
	}
	return s.drawInputLocked(s.terminalWidth())
}

func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.visible {
		return nil
	}
	s.visible = false
	_, err := io.WriteString(s.out, "\r\x1b[2K\r")
	return err
}

func (s *Screen) Feed(r rune) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, changed := s.editor.Feed(r)
	if changed && s.visible {
		_ = s.redrawInputLocked()
	}
	return event
}

func (s *Screen) SetTarget(command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := TargetLabel(command)
	if target == s.target {
		return
	}
	s.target = target
	if s.visible {
		_ = s.redrawInputLocked()
	}
}

func (s *Screen) Redraw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.visible {
		return
	}
	_ = s.clearLocked()
	_, _ = io.WriteString(s.out, "\x1b[999B\r")
	_ = s.drawInputLocked(s.terminalWidth())
}

func (s *Screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editor.Text()
}

func (s *Screen) Writer(destination io.Writer) io.Writer {
	return screenWriter{screen: s, destination: destination}
}

type screenWriter struct {
	screen      *Screen
	destination io.Writer
}

func (w screenWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s := w.screen
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.visible {
		return w.destination.Write(p)
	}
	if err := s.clearLocked(); err != nil {
		return 0, err
	}
	n, err := writeTerminalLine(w.destination, p)
	if drawErr := s.drawInputLocked(s.terminalWidth()); err == nil {
		err = drawErr
	}
	return n, err
}

func (s *Screen) clearLocked() error {
	_, err := io.WriteString(s.out, "\r\x1b[2K\r")
	return err
}

func (s *Screen) redrawInputLocked() error {
	if _, err := io.WriteString(s.out, "\r\x1b[2K\r"); err != nil {
		return err
	}
	return s.drawInputLocked(s.terminalWidth())
}

func (s *Screen) drawInputLocked(width int) error {
	plainPrefix := "[" + s.target + "] > "
	prefix := fitWidth(plainPrefix, max(width-1, 1))
	prefixWidth := runewidth.StringWidth(prefix)
	text, cursor := s.editor.Window(max(width-prefixWidth, 0))
	displayPrefix := prefix
	if prefix == plainPrefix {
		displayPrefix = "\x1b[36m[" + s.target + "]\x1b[39m \x1b[32m>\x1b[39m "
	}
	if _, err := io.WriteString(s.out, "\r"+displayPrefix+text+"\r"); err != nil {
		return err
	}
	column := prefixWidth + cursor
	if column > 0 {
		_, err := fmt.Fprintf(s.out, "\x1b[%dC", column)
		return err
	}
	return nil
}

func writeTerminalLine(destination io.Writer, p []byte) (int, error) {
	normalized := make([]byte, 0, len(p)+2)
	for i, b := range p {
		if b == '\n' && (i == 0 || p[i-1] != '\r') {
			normalized = append(normalized, '\r')
		}
		normalized = append(normalized, b)
	}
	if len(normalized) == 0 || normalized[len(normalized)-1] != '\n' {
		normalized = append(normalized, '\r', '\n')
	}
	if _, err := destination.Write(normalized); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Screen) terminalWidth() int {
	if s.width == nil {
		return defaultWidth
	}
	if width := s.width(); width > 0 {
		return width
	}
	return defaultWidth
}

func fitWidth(value string, width int) string {
	var out []rune
	used := 0
	for _, r := range value {
		w := runewidth.RuneWidth(r)
		if used+w > width {
			break
		}
		out = append(out, r)
		used += w
	}
	return string(out)
}

type Terminal struct {
	reader   *bufio.Reader
	screen   *Screen
	restore  func() error
	stop     chan struct{}
	done     chan struct{}
	resize   chan os.Signal
	close    sync.Once
	closeErr error
}

func IsInteractive(in, out *os.File) bool {
	return in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func Open(in, out *os.File, reader io.Reader) (*Terminal, error) {
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	restore := func() error { return term.Restore(int(in.Fd()), state) }
	screen := NewScreen(out, func() int {
		width, _, err := term.GetSize(int(out.Fd()))
		if err != nil {
			return defaultWidth
		}
		return width
	})
	if err = screen.Start(); err != nil {
		_ = restore()
		return nil, err
	}
	t := &Terminal{reader: bufio.NewReader(reader), screen: screen, restore: restore, stop: make(chan struct{}), done: make(chan struct{}), resize: make(chan os.Signal, 1)}
	signal.Notify(t.resize, syscall.SIGWINCH)
	go t.watchResize()
	return t, nil
}

func (t *Terminal) Next() (Event, error) {
	r, _, err := t.reader.ReadRune()
	if err != nil {
		return Event{}, err
	}
	return t.screen.Feed(r), nil
}

func (t *Terminal) SetTarget(command string) { t.screen.SetTarget(command) }
func (t *Terminal) Writer(destination io.Writer) io.Writer {
	return t.screen.Writer(destination)
}

func (t *Terminal) Close() error {
	t.close.Do(func() {
		signal.Stop(t.resize)
		close(t.stop)
		if t.done != nil {
			<-t.done
		}
		screenErr := t.screen.Close()
		restoreErr := t.restore()
		t.closeErr = errors.Join(screenErr, restoreErr)
	})
	return t.closeErr
}

func (t *Terminal) watchResize() {
	if t.done != nil {
		defer close(t.done)
	}
	for {
		select {
		case <-t.resize:
			t.screen.Redraw()
		case <-t.stop:
			return
		}
	}
}
