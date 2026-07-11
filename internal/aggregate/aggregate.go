package aggregate

import (
	"container/heap"
	"context"
	"errors"
	"github.com/SleepyMario/streamchat/internal/chat"
	"sort"
	"sync"
	"time"
)

type Config struct {
	QueueSize, DuplicateCapacity int
	ReorderWindow                time.Duration
}
type Aggregator struct {
	cfg      Config
	mu       sync.Mutex
	seen     map[string]struct{}
	order    []string
	sequence uint64
}

func New(c Config) (*Aggregator, error) {
	if c.QueueSize < 1 || c.DuplicateCapacity < 1 {
		return nil, errors.New("queue and duplicate capacities must be positive")
	}
	if c.ReorderWindow < 0 {
		return nil, errors.New("reorder window cannot be negative")
	}
	return &Aggregator{cfg: c, seen: map[string]struct{}{}}, nil
}
func (a *Aggregator) DuplicateCount() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.seen) }
func (a *Aggregator) addID(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.seen[id]; ok {
		return false
	}
	a.seen[id] = struct{}{}
	a.order = append(a.order, id)
	if len(a.order) > a.cfg.DuplicateCapacity {
		old := a.order[0]
		a.order = a.order[1:]
		delete(a.seen, old)
	}
	return true
}

type queued struct {
	msg chat.Message
	seq uint64
}
type messageHeap []queued

func (h messageHeap) Len() int { return len(h) }
func (h messageHeap) Less(i, j int) bool {
	if h[i].msg.Timestamp.Equal(h[j].msg.Timestamp) {
		if h[i].msg.Platform == h[j].msg.Platform {
			if h[i].msg.ID == h[j].msg.ID {
				return h[i].seq < h[j].seq
			}
			return h[i].msg.ID < h[j].msg.ID
		}
		return h[i].msg.Platform < h[j].msg.Platform
	}
	return h[i].msg.Timestamp.Before(h[j].msg.Timestamp)
}
func (h messageHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *messageHeap) Push(x any)   { *h = append(*h, x.(queued)) }
func (h *messageHeap) Pop() any     { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }
func (a *Aggregator) Run(ctx context.Context, inputs ...<-chan chat.Message) (<-chan chat.Message, <-chan error) {
	out := make(chan chat.Message, a.cfg.QueueSize)
	errs := make(chan error, len(inputs))
	incoming := make(chan chat.Message, a.cfg.QueueSize)
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func(ch <-chan chat.Message) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case m, ok := <-ch:
					if !ok {
						return
					}
					select {
					case incoming <- m:
					case <-ctx.Done():
						return
					}
				}
			}
		}(in)
	}
	go func() { wg.Wait(); close(incoming) }()
	go func() {
		defer close(out)
		defer close(errs)
		h := &messageHeap{}
		heap.Init(h)
		var timer *time.Timer
		flush := func() bool {
			for h.Len() > 0 {
				q := heap.Pop(h).(queued)
				select {
				case out <- q.msg:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for {
			var tc <-chan time.Time
			if timer != nil {
				tc = timer.C
			}
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case m, ok := <-incoming:
				if !ok {
					flush()
					return
				}
				if err := m.Validate(); err != nil {
					select {
					case errs <- err:
					default:
					}
					continue
				}
				if !a.addID(string(m.Platform) + ":" + m.ID) {
					continue
				}
				a.sequence++
				heap.Push(h, queued{m, a.sequence})
				if h.Len() >= a.cfg.QueueSize {
					q := heap.Pop(h).(queued)
					select {
					case out <- q.msg:
					case <-ctx.Done():
						return
					}
				}
				if timer == nil {
					timer = time.NewTimer(a.cfg.ReorderWindow)
				}
			case <-tc:
				if !flush() {
					return
				}
				timer = nil
			}
		}
	}()
	return out, errs
}
func Sort(m []chat.Message) {
	sort.SliceStable(m, func(i, j int) bool {
		if m[i].Timestamp.Equal(m[j].Timestamp) {
			if m[i].Platform == m[j].Platform {
				return m[i].ID < m[j].ID
			}
			return m[i].Platform < m[j].Platform
		}
		return m[i].Timestamp.Before(m[j].Timestamp)
	})
}
