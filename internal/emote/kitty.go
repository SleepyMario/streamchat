package emote

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const kittyRawChunkSize = 3072

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type KittyBackendOptions struct {
	TerminalOutput io.Writer
	Getenv         func(string) string
	Trace          func(string)
}

// KittyBackend renders images through Kitty's native graphics protocol. The
// placements belong to terminal cells, so normal terminal scrolling moves the
// images together with the chat instead of managing separate Wayland windows.
type KittyBackend struct {
	mu           sync.RWMutex
	out          io.Writer
	available    bool
	closed       bool
	current      map[string]Placement
	confirmed    map[string]bool
	invalidation func()
	diagnostic   func(error)
	trace        func(string)
}

func DetectKitty() bool {
	return detectKitty(os.Getenv)
}

func detectKitty(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	term := strings.ToLower(strings.TrimSpace(getenv("TERM")))
	return strings.Contains(term, "kitty") || strings.TrimSpace(getenv("KITTY_WINDOW_ID")) != ""
}

func NewKittyBackend(terminalOutput io.Writer) (*KittyBackend, error) {
	return newKittyBackend(KittyBackendOptions{TerminalOutput: terminalOutput})
}

func newKittyBackend(options KittyBackendOptions) (*KittyBackend, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if !detectKitty(getenv) {
		return nil, errors.New("Kitty graphics protocol is unavailable in this terminal")
	}
	if options.TerminalOutput == nil {
		return nil, errors.New("Kitty graphics protocol requires terminal output")
	}
	return &KittyBackend{
		out:       options.TerminalOutput,
		available: true,
		current:   make(map[string]Placement),
		confirmed: make(map[string]bool),
		trace:     options.Trace,
	}, nil
}

func (b *KittyBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available && !b.closed
}

func (b *KittyBackend) Confirmed(identifier string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.confirmed[identifier]
}

func (b *KittyBackend) SetInvalidation(invalidation func()) {
	b.mu.Lock()
	b.invalidation = invalidation
	b.mu.Unlock()
}

func (b *KittyBackend) SetDiagnostic(diagnostic func(error)) {
	b.mu.Lock()
	b.diagnostic = diagnostic
	b.mu.Unlock()
}

func (b *KittyBackend) Update(placements []Placement) {
	b.mu.Lock()
	if b.closed || !b.available {
		b.mu.Unlock()
		return
	}

	next := make(map[string]Placement, len(placements))
	for _, placement := range placements {
		if placement.Identifier == "" || placement.Path == "" {
			continue
		}
		placement.Width = max(placement.Width, 1)
		placement.Height = max(placement.Height, 1)
		next[placement.Identifier] = placement
	}

	scrollDelta, scrolled := commonScrollDelta(b.current, next)
	result := make(map[string]Placement, len(next))
	confirmedChanged := false
	for identifier, previous := range b.current {
		candidate, exists := next[identifier]
		if exists && placementsEqual(previous, candidate) {
			result[identifier] = candidate
			continue
		}
		if exists && scrolled && samePlacementExceptRow(previous, candidate) && candidate.Y-previous.Y == scrollDelta {
			// The terminal scroll operation has already moved the native image.
			result[identifier] = candidate
			continue
		}
		if err := b.deleteLocked(identifier); err != nil {
			b.reportLocked(fmt.Errorf("remove Kitty emote placement: %w", err))
		}
		if b.confirmed[identifier] {
			confirmedChanged = true
		}
		delete(b.confirmed, identifier)
	}
	for identifier, placement := range next {
		if _, retained := result[identifier]; retained {
			continue
		}
		if err := b.placeLocked(placement); err != nil {
			b.reportLocked(fmt.Errorf("display Kitty emote placement: %w", err))
			continue
		}
		result[identifier] = placement
		if !b.confirmed[identifier] {
			confirmedChanged = true
		}
		b.confirmed[identifier] = true
	}
	b.current = result
	invalidation := b.invalidation
	b.mu.Unlock()

	if confirmedChanged && invalidation != nil {
		go invalidation()
	}
}

// Reset removes placements before a full terminal redraw while retaining the
// successful-image confirmations used to replace readable fallback text.
func (b *KittyBackend) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.available {
		return
	}
	for identifier := range b.current {
		if err := b.deleteLocked(identifier); err != nil {
			b.reportLocked(fmt.Errorf("reset Kitty emote placement: %w", err))
		}
	}
	b.current = make(map[string]Placement)
}

func (b *KittyBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	var result error
	for identifier := range b.current {
		if err := b.deleteLocked(identifier); err != nil {
			result = errors.Join(result, err)
		}
	}
	b.current = make(map[string]Placement)
	b.confirmed = make(map[string]bool)
	b.closed = true
	b.available = false
	b.invalidation = nil
	return result
}

func commonScrollDelta(current, next map[string]Placement) (int, bool) {
	delta := 0
	common := 0
	for identifier, previous := range current {
		candidate, exists := next[identifier]
		if !exists || !samePlacementExceptRow(previous, candidate) {
			continue
		}
		candidateDelta := candidate.Y - previous.Y
		if common == 0 {
			delta = candidateDelta
		} else if candidateDelta != delta {
			return 0, false
		}
		common++
	}
	return delta, common > 0 && delta != 0
}

func placementsEqual(a, b Placement) bool {
	return a.Identifier == b.Identifier && a.Path == b.Path && a.X == b.X && a.Y == b.Y && a.Width == b.Width && a.Height == b.Height
}

func samePlacementExceptRow(a, b Placement) bool {
	return a.Identifier == b.Identifier && a.Path == b.Path && a.X == b.X && a.Width == b.Width && a.Height == b.Height
}

func (b *KittyBackend) placeLocked(placement Placement) error {
	data, err := os.ReadFile(placement.Path)
	if err != nil {
		return errors.New("read cached PNG")
	}
	if len(data) < len(pngSignature) || !bytes.Equal(data[:len(pngSignature)], pngSignature) {
		return errors.New("cached emote is not PNG")
	}
	imageID := kittyImageID(placement.Identifier)
	if _, err = fmt.Fprintf(b.out, "\x1b7\x1b[%d;%dH", placement.Y+1, placement.X+1); err != nil {
		return errors.New("position terminal cursor")
	}
	for offset := 0; offset < len(data); offset += kittyRawChunkSize {
		end := min(offset+kittyRawChunkSize, len(data))
		payload := base64.StdEncoding.EncodeToString(data[offset:end])
		more := 0
		if end < len(data) {
			more = 1
		}
		control := fmt.Sprintf("m=%d,q=2", more)
		if offset == 0 {
			control = fmt.Sprintf("a=T,f=100,i=%d,p=1,c=%d,r=%d,C=1,m=%d,q=2", imageID, placement.Width, placement.Height, more)
		}
		if _, err = fmt.Fprintf(b.out, "\x1b_G%s;%s\x1b\\", control, payload); err != nil {
			_, _ = io.WriteString(b.out, "\x1b8")
			return errors.New("transmit PNG to Kitty")
		}
	}
	if _, err = io.WriteString(b.out, "\x1b8"); err != nil {
		return errors.New("restore terminal cursor")
	}
	b.tracef("placement ready: identifier=%s image_id=%d x=%d y=%d width=%d height=%d", placement.Identifier, imageID, placement.X, placement.Y, placement.Width, placement.Height)
	return nil
}

func (b *KittyBackend) deleteLocked(identifier string) error {
	imageID := kittyImageID(identifier)
	_, err := fmt.Fprintf(b.out, "\x1b_Ga=d,d=I,i=%d,q=2;\x1b\\", imageID)
	if err != nil {
		return errors.New("send Kitty deletion command")
	}
	b.tracef("placement removed: identifier=%s image_id=%d", identifier, imageID)
	return nil
}

func kittyImageID(identifier string) uint32 {
	digest := sha256.Sum256([]byte("streamchat-kitty\x00" + identifier))
	value := binary.BigEndian.Uint32(digest[:4])
	if value == 0 {
		return 1
	}
	return value
}

func (b *KittyBackend) reportLocked(err error) {
	if b.diagnostic != nil {
		b.diagnostic(err)
	}
	if b.trace != nil {
		b.trace(err.Error())
	}
}

func (b *KittyBackend) tracef(format string, values ...any) {
	if b.trace != nil {
		b.trace(fmt.Sprintf(format, values...))
	}
}
