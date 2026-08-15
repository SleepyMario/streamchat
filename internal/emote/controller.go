package emote

import (
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

type ControllerOptions struct {
	Mode    string
	Cache   *Cache
	Backend Backend
}

// Controller connects persistent assets to an optional image backend. It is
// provider-neutral; adapters supply URLs on structured chat.Emote values.
type Controller struct {
	mode     string
	cache    *Cache
	backend  Backend
	mu       sync.RWMutex
	redraw   func()
	close    sync.Once
	closeErr error
}

func NewController(options ControllerOptions) *Controller {
	controller := &Controller{mode: options.Mode, cache: options.Cache, backend: options.Backend}
	if controller.mode == "" {
		controller.mode = "auto"
	}
	if invalidation, ok := controller.backend.(invalidationBackend); ok {
		invalidation.SetInvalidation(controller.requestRedraw)
	}
	return controller
}

func NewDefaultController(mode string, terminalOutput ...io.Writer) *Controller {
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" {
		return NewController(ControllerOptions{Mode: mode})
	}
	cache, err := NewCache(CacheOptions{})
	if err != nil {
		return NewController(ControllerOptions{Mode: mode})
	}
	var output io.Writer
	if len(terminalOutput) > 0 {
		output = terminalOutput[0]
	}
	backend, err := NewUeberzugBackend(output)
	if err != nil {
		return NewController(ControllerOptions{Mode: mode, Cache: cache})
	}
	return NewController(ControllerOptions{Mode: mode, Cache: cache, Backend: backend})
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
	return c.cache.Resolve(string(platform), item.ID, item.URL, func(err error) {
		if err == nil {
			c.requestRedraw()
		}
	})
}

func (c *Controller) Available() bool {
	return c != nil && c.mode == "auto" && c.backend != nil && c.backend.Available()
}

func (c *Controller) Update(placements []Placement) {
	if c != nil && c.backend != nil && c.backend.Available() {
		c.backend.Update(placements)
	}
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
	})
	return c.closeErr
}

func (c *Controller) requestRedraw() {
	c.mu.RLock()
	redraw := c.redraw
	c.mu.RUnlock()
	if redraw != nil {
		redraw()
	}
}
