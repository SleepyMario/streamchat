package emote

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/SleepyMario/streamchat/internal/chat"
)

type Placement struct {
	Identifier string
	Path       string
	X          int
	Y          int
	Width      int
	Height     int
}

type Backend interface {
	Available() bool
	Update([]Placement)
	Close() error
}

type invalidationBackend interface {
	SetInvalidation(func())
}

type diagnosticBackend interface {
	SetDiagnostic(func(error))
}

type resetBackend interface {
	Reset()
}

type ControllerOptions struct {
	Mode    string
	Cache   *Cache
	Backend Backend
}

type DefaultControllerOptions struct {
	Mode           string
	TerminalOutput io.Writer
	Debug          bool
}

// Controller connects persistent assets to an optional image backend. It is
// provider-neutral; adapters supply URLs on structured chat.Emote values.
type Controller struct {
	mode       string
	cache      *Cache
	backend    Backend
	mu         sync.RWMutex
	redraw     func()
	close      sync.Once
	closeErr   error
	diagnostic *diagnosticLog
	debugSeen  map[string]struct{}
}

func NewController(options ControllerOptions) *Controller {
	controller := &Controller{mode: options.Mode, cache: options.Cache, backend: options.Backend}
	if controller.mode == "" {
		controller.mode = "auto"
	}
	if invalidation, ok := controller.backend.(invalidationBackend); ok {
		invalidation.SetInvalidation(controller.requestRedraw)
	}
	if diagnostic, ok := controller.backend.(diagnosticBackend); ok {
		diagnostic.SetDiagnostic(func(err error) { controller.debug("backend failure: " + err.Error()) })
	}
	return controller
}

func NewDefaultController(mode string, terminalOutput ...io.Writer) *Controller {
	options := DefaultControllerOptions{Mode: mode}
	if len(terminalOutput) > 0 {
		options.TerminalOutput = terminalOutput[0]
	}
	return NewDefaultControllerWithOptions(options)
}

func NewDefaultControllerWithOptions(options DefaultControllerOptions) *Controller {
	mode := options.Mode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" {
		return NewController(ControllerOptions{Mode: mode})
	}
	// Avoid creating cache or diagnostic state when this terminal cannot use
	// the native backend; readable provider names need no graphical resources.
	if !DetectKitty() {
		return NewController(ControllerOptions{Mode: mode})
	}
	cache, err := NewCache(CacheOptions{})
	if err != nil {
		return NewController(ControllerOptions{Mode: mode})
	}
	var diagnostic *diagnosticLog
	if options.Debug {
		diagnostic, _ = newDiagnosticLog(cache.directory)
	}
	trace := func(message string) {
		if diagnostic != nil {
			diagnostic.write("kitty: " + message)
		}
	}
	backend, err := newKittyBackend(KittyBackendOptions{TerminalOutput: options.TerminalOutput, Trace: trace})
	if err != nil {
		controller := NewController(ControllerOptions{Mode: mode, Cache: cache})
		controller.diagnostic = diagnostic
		controller.debug("backend unavailable: " + err.Error())
		return controller
	}
	controller := NewController(ControllerOptions{Mode: mode, Cache: cache, Backend: backend})
	controller.diagnostic = diagnostic
	controller.debugSeen = make(map[string]struct{})
	backend.SetDiagnostic(func(err error) { controller.debug("backend failure: " + err.Error()) })
	if backend.Available() {
		controller.debug("backend ready: Kitty graphics protocol")
	} else {
		controller.debug("backend unavailable: Kitty graphics protocol")
	}
	return controller
}

func (c *Controller) SetRedraw(redraw func()) {
	c.mu.Lock()
	c.redraw = redraw
	c.mu.Unlock()
}

func (c *Controller) Resolve(platform chat.Platform, item chat.Emote) (string, bool) {
	if c == nil || c.mode != "auto" || c.cache == nil || c.backend == nil || !c.backend.Available() {
		return "", false
	}
	key := string(platform) + ":" + item.ID
	path, ok, state := c.cache.ResolveDetailed(string(platform), item.ID, item.URL, func(err error) {
		if err == nil {
			c.debugOnce(key+":downloaded", fmt.Sprintf("download complete: provider=%s id=%s", platform, item.ID))
			c.requestRedraw()
			return
		}
		c.debugOnce(key+":failed", fmt.Sprintf("download failed: provider=%s id=%s error=%s", platform, item.ID, err))
	})
	switch state {
	case CacheHit:
		c.debugOnce(key+":hit", fmt.Sprintf("cache hit: provider=%s id=%s", platform, item.ID))
		if ok {
			staticPath, staticErr := staticAsset(path)
			if staticErr != nil {
				c.debugOnce(key+":static-failed", fmt.Sprintf("static preview failed: provider=%s id=%s error=%s", platform, item.ID, staticErr))
				return "", false
			}
			path = staticPath
		}
	case CacheQueued:
		c.debugOnce(key+":queued", fmt.Sprintf("download queued: provider=%s id=%s", platform, item.ID))
	case CacheInvalid:
		c.debugOnce(key+":invalid", fmt.Sprintf("cache skipped invalid emote metadata: provider=%s id=%s", platform, item.ID))
	case CacheBackoff:
		c.debugOnce(key+":backoff", fmt.Sprintf("download retry deferred: provider=%s id=%s", platform, item.ID))
	}
	return path, ok
}

func (c *Controller) Confirmed(identifier string) bool {
	if c == nil {
		return false
	}
	if backend, ok := c.backend.(interface{ Confirmed(string) bool }); ok {
		return backend.Confirmed(identifier)
	}
	return false
}

func (c *Controller) Available() bool {
	return c != nil && c.mode == "auto" && c.backend != nil && c.backend.Available()
}

func (c *Controller) Update(placements []Placement) {
	if c != nil && c.backend != nil && c.backend.Available() {
		c.backend.Update(placements)
	}
}

func (c *Controller) Reset() {
	if c == nil {
		return
	}
	if backend, ok := c.backend.(resetBackend); ok {
		backend.Reset()
		return
	}
	c.Update(nil)
}

func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	c.close.Do(func() {
		c.mu.Lock()
		c.redraw = nil
		c.mu.Unlock()
		if c.backend != nil {
			c.closeErr = c.backend.Close()
		}
		c.debug("controller closed")
		c.closeErr = errors.Join(c.closeErr, c.diagnostic.close())
	})
	return c.closeErr
}

func (c *Controller) debug(message string) {
	if c != nil && c.diagnostic != nil {
		c.diagnostic.write(message)
	}
}

func (c *Controller) debugOnce(key, message string) {
	if c == nil || c.diagnostic == nil {
		return
	}
	c.mu.Lock()
	if c.debugSeen == nil {
		c.debugSeen = make(map[string]struct{})
	}
	_, seen := c.debugSeen[key]
	if !seen {
		c.debugSeen[key] = struct{}{}
	}
	c.mu.Unlock()
	if !seen {
		c.debug(message)
	}
}

func (c *Controller) requestRedraw() {
	c.mu.RLock()
	redraw := c.redraw
	c.mu.RUnlock()
	if redraw != nil {
		redraw()
	}
}
