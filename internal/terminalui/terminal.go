package terminalui

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/SleepyMario/streamchat/internal/command"
	"github.com/SleepyMario/streamchat/internal/emote"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const (
	defaultWidth         = 80
	defaultHeight        = 24
	displayMessageLimit  = 500
	statusLineCount      = 3
	maxSuggestionRows    = 5
	clockRefreshInterval = time.Minute
	enterAlternateScreen = "\x1b[?1049h\x1b[2J\x1b[H"
	leaveAlternateScreen = "\x1b[0m\x1b[?1049l"
)

type DisplayMessage struct {
	ID          string
	Platform    string
	Author      string
	Line        string
	Render      func() emote.Line
	leadingRows int
	imageKey    uint64
}

// StreamStatus is provider-neutral metadata displayed above chat.
type StreamStatus struct {
	Title       string
	Category    string
	ViewerCount int
	Live        bool
}

type Screen struct {
	mu            sync.Mutex
	out           io.Writer
	width         func() int
	height        func() int
	now           func() time.Time
	editor        Editor
	target        string
	messages      []DisplayMessage
	status        StreamStatus
	known         bool
	visible       bool
	images        emote.Backend
	transientRows int
	drawnSuggest  int
	nextImageKey  uint64
}

type screenLayout struct {
	statusRows         int
	topSeparatorRow    int
	chatTop            int
	chatBottom         int
	suggestionTop      int
	suggestionBottom   int
	bottomSeparatorRow int
	inputRow           int
}

func NewScreen(out io.Writer, width func() int) *Screen {
	return newScreen(out, width, func() int { return defaultHeight })
}

func newScreen(out io.Writer, width, height func() int) *Screen {
	return newScreenWithBackend(out, width, height, nil)
}

func newScreenWithBackend(out io.Writer, width, height func() int, images emote.Backend) *Screen {
	return &Screen{out: out, width: width, height: height, now: func() time.Time { return time.Now().In(time.Local) }, editor: NewEditor(command.Streamchat()), target: "NONE", images: images}
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
	if s.images != nil {
		s.images.Update(nil)
	}
	s.visible = false
	_, err := io.WriteString(s.out, "\x1b[r\x1b[0m"+leaveAlternateScreen)
	return err
}

func (s *Screen) closeImages() error {
	s.mu.Lock()
	images := s.images
	s.images = nil
	s.mu.Unlock()
	if images == nil {
		return nil
	}
	images.Update(nil)
	return images.Close()
}

func (s *Screen) Feed(r rune) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, changed := s.editor.Feed(r)
	if changed && s.visible {
		_ = s.redrawEditorLocked()
	}
	return event
}

func (s *Screen) DismissSuggestions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editor.DismissSuggestions() && s.visible {
		_ = s.redrawEditorLocked()
	}
}

func (s *Screen) HasSuggestions() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	suggestions, _ := s.editor.Suggestions()
	return len(suggestions) > 0
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

func (s *Screen) RefreshTime() {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.nextImageKey++
	message.imageKey = s.nextImageKey
	message.leadingRows = s.transientRows
	s.transientRows = 0
	s.messages = append(s.messages, message)
	if len(s.messages) > displayMessageLimit {
		copy(s.messages, s.messages[len(s.messages)-displayMessageLimit:])
		s.messages = s.messages[:displayMessageLimit]
	}
	if !s.visible {
		return nil
	}
	width, height := s.terminalWidth(), s.terminalHeight()
	if err := s.appendChatLocked([]byte(s.renderedForDisplayLocked(message).Text), height); err != nil {
		return err
	}
	if err := s.drawInputLocked(width, height); err != nil {
		return err
	}
	s.refreshImagesLocked(width, height)
	return nil
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
	s.transientRows = 0
	for index := range s.messages {
		s.messages[index].leadingRows = 0
	}
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
		if err := s.appendChatLocked([]byte(s.renderedForDisplayLocked(message).Text), height); err != nil {
			return err
		}
	}
	if err := s.drawSuggestionsLocked(width, height); err != nil {
		return err
	}
	if err := s.drawInputLocked(width, height); err != nil {
		return err
	}
	s.refreshImagesLocked(width, height)
	return nil
}

func (s *Screen) redrawChatLocked() error {
	width, height := s.terminalWidth(), s.terminalHeight()
	s.transientRows = 0
	for index := range s.messages {
		s.messages[index].leadingRows = 0
	}
	layout := s.layoutLocked(height)
	baseLayout := layoutForHeight(height)
	if err := s.setChatRegionLocked(height); err != nil {
		return err
	}
	for row := layout.chatTop; row > 0 && row <= baseLayout.chatBottom; row++ {
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K", row); err != nil {
			return err
		}
	}
	for _, message := range s.messages {
		if err := s.appendChatLocked([]byte(s.renderedForDisplayLocked(message).Text), height); err != nil {
			return err
		}
	}
	if err := s.drawSuggestionsLocked(width, height); err != nil {
		return err
	}
	if err := s.drawInputLocked(width, height); err != nil {
		return err
	}
	s.refreshImagesLocked(width, height)
	return nil
}

func (m DisplayMessage) rendered() emote.Line {
	if m.Render != nil {
		return m.Render()
	}
	return emote.Line{Text: m.Line}
}

func (s *Screen) renderedForDisplayLocked(message DisplayMessage) emote.Line {
	line := message.rendered()
	if line.GraphicalText == "" || len(line.Images) == 0 {
		return line
	}
	confirmations, ok := s.images.(interface{ Confirmed(string) bool })
	if !ok {
		return line
	}
	for imageIndex := range line.Images {
		if !confirmations.Confirmed(imageIdentifier(message, imageIndex)) {
			return line
		}
	}
	line.Text = line.GraphicalText
	return line
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
	s.transientRows += wrappedRows(string(p), width)
	n := len(p)
	var err error
	if drawErr := s.drawInputLocked(width, height); err == nil {
		err = drawErr
	}
	s.refreshImagesLocked(width, height)
	return n, err
}

func (s *Screen) redrawInputLocked() error {
	return s.drawInputLocked(s.terminalWidth(), s.terminalHeight())
}

func (s *Screen) redrawEditorLocked() error {
	width, height := s.terminalWidth(), s.terminalHeight()
	if rows := s.suggestionRowsLocked(height); rows != s.drawnSuggest {
		return s.redrawChatLocked()
	}
	if err := s.drawSuggestionsLocked(width, height); err != nil {
		return err
	}
	return s.drawInputLocked(width, height)
}

func (s *Screen) drawSuggestionsLocked(width, height int) error {
	suggestions, selected := s.editor.Suggestions()
	layout := s.layoutLocked(height)
	rows := 0
	if layout.suggestionTop > 0 {
		rows = layout.suggestionBottom - layout.suggestionTop + 1
	}
	s.drawnSuggest = rows
	if rows <= 0 {
		return nil
	}
	start := 0
	if selected >= rows {
		start = selected - rows + 1
	}
	if start+rows > len(suggestions) {
		start = max(len(suggestions)-rows, 0)
	}
	for index := 0; index < rows; index++ {
		candidateIndex := start + index
		marker := "  "
		if candidateIndex == selected {
			marker = "> "
		}
		line := fitWidth(marker+suggestions[candidateIndex], max(width, 1))
		row := layout.suggestionTop + index
		if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", row, line); err != nil {
			return err
		}
	}
	return nil
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
	now := s.currentTime()
	right := []string{now.Format("2006-01-02"), now.Format("15:04"), ""}
	rows := layoutForHeight(height).statusRows
	for i := 0; i < rows; i++ {
		plain := statusLine(labels[i]+values[i], right[i], max(width, 1))
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

func statusLine(left, right string, width int) string {
	width = max(width, 1)
	if right == "" {
		return fitWidth(left, width)
	}
	right = fitWidth(right, width)
	rightWidth := runewidth.StringWidth(right)
	if rightWidth >= width {
		return right
	}
	left = fitWidth(left, max(width-rightWidth-1, 0))
	spaces := width - runewidth.StringWidth(left) - rightWidth
	return left + strings.Repeat(" ", spaces) + right
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
	layout := s.layoutLocked(height)
	if layout.chatTop > 0 && layout.chatTop < layout.chatBottom {
		_, err := fmt.Fprintf(s.out, "\x1b[%d;%dr", layout.chatTop, layout.chatBottom)
		return err
	}
	_, err := io.WriteString(s.out, "\x1b[r")
	return err
}

func (s *Screen) appendChatLocked(p []byte, height int) error {
	layout := s.layoutLocked(height)
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

var terminalANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func (s *Screen) refreshImagesLocked(width, height int) {
	if s.images == nil {
		return
	}
	layout := s.layoutLocked(height)
	capacity := layout.chatBottom - layout.chatTop + 1
	if capacity <= 0 {
		s.images.Update(nil)
		return
	}
	type renderedMessage struct {
		message DisplayMessage
		line    emote.Line
		start   int
		rows    int
	}
	rendered := make([]renderedMessage, 0, len(s.messages))
	totalRows := 0
	for _, message := range s.messages {
		totalRows += message.leadingRows
		line := message.rendered()
		layoutText := line.Text
		if line.GraphicalText != "" {
			layoutText = line.GraphicalText
		}
		displayWidth := runewidth.StringWidth(terminalANSI.ReplaceAllString(strings.TrimRight(layoutText, "\r\n"), ""))
		rows := max((max(displayWidth, 1)-1)/max(width, 1)+1, 1)
		rendered = append(rendered, renderedMessage{message: message, line: line, start: totalRows, rows: rows})
		totalRows += rows
	}
	totalRows += s.transientRows
	visibleStart := max(totalRows-capacity, 0)
	bottomPadding := max(capacity-totalRows, 0)
	placements := make([]emote.Placement, 0)
	for _, item := range rendered {
		for imageIndex, image := range item.line.Images {
			rowOffset := image.Column / max(width, 1)
			x := image.Column % max(width, 1)
			if x+image.Width > width {
				rowOffset++
				x = 0
			}
			globalRow := item.start + rowOffset
			if globalRow < visibleStart || globalRow >= totalRows || rowOffset >= item.rows {
				continue
			}
			y := layout.chatTop + bottomPadding + globalRow - visibleStart
			placements = append(placements, emote.Placement{Identifier: imageIdentifier(item.message, imageIndex), Path: image.Path, X: x, Y: y - 1, Width: max(image.Width, 1), Height: 1})
		}
	}
	s.images.Update(placements)
}

func imageIdentifier(message DisplayMessage, imageIndex int) string {
	digest := sha256.Sum256([]byte(message.Platform + "\x00" + message.ID + "\x00" + strconv.FormatUint(message.imageKey, 10)))
	return fmt.Sprintf("streamchat-%x-%d", digest[:8], imageIndex)
}

func wrappedRows(value string, width int) int {
	value = strings.TrimRight(value, "\r\n")
	lines := strings.Split(value, "\n")
	rows := 0
	for _, line := range lines {
		line = terminalANSI.ReplaceAllString(strings.TrimSuffix(line, "\r"), "")
		displayWidth := runewidth.StringWidth(line)
		rows += max((max(displayWidth, 1)-1)/max(width, 1)+1, 1)
	}
	return rows
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

func (s *Screen) layoutLocked(height int) screenLayout {
	layout := layoutForHeight(height)
	rows := s.suggestionRowsLocked(height)
	if rows == 0 {
		return layout
	}
	layout.suggestionBottom = layout.chatBottom
	layout.suggestionTop = layout.suggestionBottom - rows + 1
	layout.chatBottom = layout.suggestionTop - 1
	return layout
}

func (s *Screen) suggestionRowsLocked(height int) int {
	suggestions, _ := s.editor.Suggestions()
	if len(suggestions) == 0 {
		return 0
	}
	layout := layoutForHeight(height)
	capacity := layout.chatBottom - layout.chatTop + 1
	if capacity <= 1 {
		return 0
	}
	return min(len(suggestions), maxSuggestionRows, capacity-1)
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

func (s *Screen) currentTime() time.Time {
	if s.now == nil {
		return time.Now().In(time.Local)
	}
	return s.now()
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
	reader    *bufio.Reader
	screen    *Screen
	restore   func() error
	stop      chan struct{}
	done      chan struct{}
	resize    chan os.Signal
	clock     <-chan time.Time
	stopClock func()
	close     sync.Once
	closeErr  error
}

func IsInteractive(in, out *os.File) bool {
	return in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func Open(in, out *os.File, reader io.Reader) (*Terminal, error) {
	return OpenWithBackend(in, out, reader, nil)
}

func OpenWithBackend(in, out *os.File, reader io.Reader, images emote.Backend) (*Terminal, error) {
	return OpenWithBackendAndTarget(in, out, reader, images, "")
}

func OpenWithBackendAndTarget(in, out *os.File, reader io.Reader, images emote.Backend, target string) (*Terminal, error) {
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	restore := func() error { return term.Restore(int(in.Fd()), state) }
	screen := newScreenWithBackend(out, func() int {
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
	}, images)
	screen.SetTarget(target)
	if err = startScreen(screen, restore); err != nil {
		if images != nil {
			_ = images.Close()
		}
		return nil, err
	}
	clock := time.NewTicker(clockRefreshInterval)
	t := &Terminal{reader: bufio.NewReader(reader), screen: screen, restore: restore, stop: make(chan struct{}), done: make(chan struct{}), resize: make(chan os.Signal, 1), clock: clock.C, stopClock: clock.Stop}
	signal.Notify(t.resize, syscall.SIGWINCH)
	go t.watchScreen()
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
	if r == 0x1b && t.reader.Buffered() == 0 {
		t.screen.DismissSuggestions()
		return Event{}, nil
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
func (t *Terminal) Redraw() { t.screen.Redraw() }

func (t *Terminal) Close() error {
	t.close.Do(func() {
		signal.Stop(t.resize)
		if t.stopClock != nil {
			t.stopClock()
		}
		close(t.stop)
		if t.done != nil {
			<-t.done
		}
		imagesErr := t.screen.closeImages()
		screenErr := t.screen.Close()
		restoreErr := t.restore()
		t.closeErr = errors.Join(screenErr, imagesErr, restoreErr)
	})
	return t.closeErr
}

func (t *Terminal) watchScreen() {
	if t.done != nil {
		defer close(t.done)
	}
	for {
		select {
		case <-t.resize:
			t.screen.Redraw()
		case <-t.clock:
			t.screen.RefreshTime()
		case <-t.stop:
			return
		}
	}
}
