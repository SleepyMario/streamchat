package terminalui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const (
	defaultWidth         = 80
	defaultHeight        = 24
	displayMessageLimit  = 500
	statusLineCount      = 3
	enterAlternateScreen = "\x1b[?1049h\x1b[2J\x1b[H"
	leaveAlternateScreen = "\x1b[0m\x1b[?1049l"
)

type DisplayMessage struct {
	ID       string
	Platform string
	Author   string
	Line     string
}

// StreamStatus is provider-neutral metadata displayed above chat.
type StreamStatus struct {
	Title       string
	Category    string
	ViewerCount int
	Live        bool
}

type Screen struct {
	mu       sync.Mutex
	out      io.Writer
	width    func() int
	height   func() int
	editor   Editor
	target   string
	messages []DisplayMessage
	status   StreamStatus
	known    bool
	visible  bool
}

type screenLayout struct {
	statusRows         int
	topSeparatorRow    int
	chatTop            int
	chatBottom         int
	bottomSeparatorRow int
	inputRow           int
}

func NewScreen(out io.Writer, width func() int) *Screen {
	return newScreen(out, width, func() int { return defaultHeight })
}

func newScreen(out io.Writer, width, height func() int) *Screen {
	return &Screen{out: out, width: width, height: height, target: "NONE"}
}

func (s *Screen) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.visible {
		return nil
	}
	s.visible = true
	if _, err := io.WriteString(s.out, enterAlternateScreen); err != nil {
		return err
	}
	return s.redrawViewLocked()
}

func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.visible {
		return nil
	}
	s.visible = false
	_, err := io.WriteString(s.out, "\x1b[r\x1b[0m"+leaveAlternateScreen)
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
	_ = s.redrawViewLocked()
}

func (s *Screen) SetStatus(status StreamStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.known = true
	if !s.visible {
		return
	}
	width, height := s.terminalWidth(), s.terminalHeight()
	_ = s.drawStatusLocked(width, height)
	_ = s.drawInputLocked(width, height)
}

func (s *Screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editor.Text()
}

func (s *Screen) AppendMessage(message DisplayMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	if len(s.messages) > displayMessageLimit {
		copy(s.messages, s.messages[len(s.messages)-displayMessageLimit:])
		s.messages = s.messages[:displayMessageLimit]
	}
	if !s.visible {
		return nil
	}
	width, height := s.terminalWidth(), s.terminalHeight()
	if err := s.appendChatLocked([]byte(message.Line), height); err != nil {
		return err
	}
	return s.drawInputLocked(width, height)
}

func (s *Screen) CleanAll() (int, error) {
	return s.cleanMessages(func(DisplayMessage) bool { return true })
}

func (s *Screen) CleanPlatform(platform string) (int, error) {
	return s.cleanMessages(func(message DisplayMessage) bool {
		return strings.EqualFold(message.Platform, platform)
	})
}

func (s *Screen) CleanAuthor(author string) (int, error) {
	return s.cleanMessages(func(message DisplayMessage) bool {
		return strings.EqualFold(message.Author, author)
	})
}

// KnownMessageIDs returns a point-in-time, deduplicated snapshot of provider
// message IDs in the bounded display buffer.
func (s *Screen) KnownMessageIDs(platform string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, message := range s.messages {
		id := strings.TrimSpace(message.ID)
		if id == "" || !strings.EqualFold(message.Platform, platform) {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Screen) cleanMessages(matches func(DisplayMessage) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.messages[:0]
	removed := 0
	for _, message := range s.messages {
		if matches(message) {
			removed++
			continue
		}
		kept = append(kept, message)
	}
	s.messages = kept
	if removed == 0 || !s.visible {
		return removed, nil
	}
	return removed, s.redrawChatLocked()
}

func (s *Screen) redrawViewLocked() error {
	width, height := s.terminalWidth(), s.terminalHeight()
	if _, err := io.WriteString(s.out, "\x1b[r\x1b[2J\x1b[H"); err != nil {
		return err
	}
	if err := s.drawStatusLocked(width, height); err != nil {
		return err
	}
	if err := s.drawSeparatorsLocked(width, height); err != nil {
		return err
	}
	if err := s.setChatRegionLocked(height); err != nil {
		return err
	}
	for _, message := range s.messages {
		if err := s.appendChatLocked([]byte(message.Line), height); err != nil {
			return err
		}
	}
	return s.drawInputLocked(width, height)
}

func (s *Screen) redrawChatLocked() error {
	width, height := s.terminalWidth(), s.terminalHeight()
	layout := layoutForHeight(height)
	if err := s.setChatRegionLocked(height); err != nil {
		return err
	}
	for row := layout.chatTop; row > 0 && row <= layout.chatBottom; row++ {
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K", row); err != nil {
			return err
		}
	}
	for _, message := range s.messages {
		if err := s.appendChatLocked([]byte(message.Line), height); err != nil {
			return err
		}
	}
	return s.drawInputLocked(width, height)
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
	width, height := s.terminalWidth(), s.terminalHeight()
	if err := s.appendChatLocked(p, height); err != nil {
		return 0, err
	}
	n := len(p)
	var err error
	if drawErr := s.drawInputLocked(width, height); err == nil {
		err = drawErr
	}
	return n, err
}

func (s *Screen) redrawInputLocked() error {
	return s.drawInputLocked(s.terminalWidth(), s.terminalHeight())
}

func (s *Screen) drawInputLocked(width, height int) error {
	plainPrefix := "[" + s.target + "] > "
	prefix := fitWidth(plainPrefix, max(width-1, 1))
	prefixWidth := runewidth.StringWidth(prefix)
	text, cursor := s.editor.Window(max(width-prefixWidth, 0))
	displayPrefix := prefix
	if prefix == plainPrefix {
		displayPrefix = "\x1b[36m[" + s.target + "]\x1b[39m \x1b[32m>\x1b[39m "
	}
	row := layoutForHeight(height).inputRow
	if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s%s\r", row, displayPrefix, text); err != nil {
		return err
	}
	column := prefixWidth + cursor
	if column > 0 {
		_, err := fmt.Fprintf(s.out, "\x1b[%dC", column)
		return err
	}
	return nil
}

func (s *Screen) drawStatusLocked(width, height int) error {
	values := []string{"unavailable", "unavailable", "unavailable"}
	if s.known {
		values[0] = statusValue(s.status.Title)
		values[1] = statusValue(s.status.Category)
		if s.status.Live {
			values[2] = strconv.Itoa(max(s.status.ViewerCount, 0))
		} else {
			values[2] = "OFFLINE"
		}
	}
	labels := []string{"Title:    ", "Category: ", "Viewers:  "}
	rows := layoutForHeight(height).statusRows
	for i := 0; i < rows; i++ {
		plain := fitWidth(labels[i]+values[i], max(width, 1))
		display := plain
		if strings.HasPrefix(plain, labels[i]) {
			display = "\x1b[36m" + strings.TrimRight(labels[i], " ") + "\x1b[39m" + labels[i][len(strings.TrimRight(labels[i], " ")):] + strings.TrimPrefix(plain, labels[i])
		}
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", i+1, display); err != nil {
			return err
		}
	}
	return nil
}

func (s *Screen) drawSeparatorsLocked(width, height int) error {
	layout := layoutForHeight(height)
	separator := strings.Repeat("-", max(width, 1))
	for _, row := range []int{layout.topSeparatorRow, layout.bottomSeparatorRow} {
		if row == 0 {
			continue
		}
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", row, separator); err != nil {
			return err
		}
	}
	return nil
}

func statusValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unavailable"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, value)
}

func (s *Screen) setChatRegionLocked(height int) error {
	layout := layoutForHeight(height)
	if layout.chatTop > 0 && layout.chatTop < layout.chatBottom {
		_, err := fmt.Fprintf(s.out, "\x1b[%d;%dr", layout.chatTop, layout.chatBottom)
		return err
	}
	_, err := io.WriteString(s.out, "\x1b[r")
	return err
}

func (s *Screen) appendChatLocked(p []byte, height int) error {
	layout := layoutForHeight(height)
	if layout.chatTop == 0 {
		return nil
	}
	value := strings.TrimRight(string(p), "\r\n")
	lines := strings.Split(value, "\n")
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if layout.chatTop == layout.chatBottom {
			if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", layout.chatBottom, line); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1bD\r\x1b[2K%s", layout.chatBottom, line); err != nil {
			return err
		}
	}
	return nil
}

func layoutForHeight(height int) screenLayout {
	height = max(height, 1)
	layout := screenLayout{
		statusRows: min(statusLineCount, height-1),
		inputRow:   height,
	}
	available := height - layout.statusRows - 1
	if available >= 1 {
		layout.topSeparatorRow = layout.statusRows + 1
	}
	if available >= 2 {
		layout.bottomSeparatorRow = height - 1
	}
	if available >= 3 {
		layout.chatTop = layout.topSeparatorRow + 1
		layout.chatBottom = layout.bottomSeparatorRow - 1
	}
	return layout
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

func (s *Screen) terminalHeight() int {
	if s.height == nil {
		return defaultHeight
	}
	if height := s.height(); height > 0 {
		return height
	}
	return defaultHeight
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
	screen := newScreen(out, func() int {
		width, _, err := term.GetSize(int(out.Fd()))
		if err != nil {
			return defaultWidth
		}
		return width
	}, func() int {
		_, height, err := term.GetSize(int(out.Fd()))
		if err != nil {
			return defaultHeight
		}
		return height
	})
	if err = startScreen(screen, restore); err != nil {
		return nil, err
	}
	t := &Terminal{reader: bufio.NewReader(reader), screen: screen, restore: restore, stop: make(chan struct{}), done: make(chan struct{}), resize: make(chan os.Signal, 1)}
	signal.Notify(t.resize, syscall.SIGWINCH)
	go t.watchResize()
	return t, nil
}

func startScreen(screen *Screen, restore func() error) error {
	if err := screen.Start(); err != nil {
		screenErr := screen.Close()
		restoreErr := restore()
		return errors.Join(err, screenErr, restoreErr)
	}
	return nil
}

func (t *Terminal) Next() (Event, error) {
	r, _, err := t.reader.ReadRune()
	if err != nil {
		return Event{}, err
	}
	return t.screen.Feed(r), nil
}

func (t *Terminal) SetTarget(command string)      { t.screen.SetTarget(command) }
func (t *Terminal) SetStatus(status StreamStatus) { t.screen.SetStatus(status) }
func (t *Terminal) AppendMessage(message DisplayMessage) error {
	return t.screen.AppendMessage(message)
}
func (t *Terminal) CleanAll() (int, error) { return t.screen.CleanAll() }
func (t *Terminal) CleanPlatform(platform string) (int, error) {
	return t.screen.CleanPlatform(platform)
}
func (t *Terminal) CleanAuthor(author string) (int, error) {
	return t.screen.CleanAuthor(author)
}
func (t *Terminal) KnownMessageIDs(platform string) []string {
	return t.screen.KnownMessageIDs(platform)
}
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
