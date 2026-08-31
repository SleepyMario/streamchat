package discordnotify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

type fakeSender struct {
	mu       sync.Mutex
	messages []string
}

func (s *fakeSender) Send(_ context.Context, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func TestWatcherSuppressesRestartAndDeduplicatesSession(t *testing.T) {
	var mu sync.RWMutex
	state := streamprobe.State{}
	set := func(online bool, n int) {
		mu.Lock()
		state = streamprobe.State{Online: online, CheckedAt: time.Unix(int64(n), 0)}
		mu.Unlock()
	}
	get := func() streamprobe.State {
		mu.RLock()
		defer mu.RUnlock()
		return state
	}
	sender := &fakeSender{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (Watcher{Sender: sender, State: get, BuildMessage: func(context.Context) (string, error) { return "live", nil }, PollInterval: time.Millisecond, Confirmations: 2}).Run(ctx)

	set(true, 1)
	time.Sleep(10 * time.Millisecond)
	set(true, 2)
	time.Sleep(10 * time.Millisecond)
	if sender.count() != 0 {
		t.Fatal("restart during an existing stream was announced")
	}

	set(false, 3)
	time.Sleep(10 * time.Millisecond)
	set(true, 4)
	time.Sleep(10 * time.Millisecond)
	set(true, 5)
	time.Sleep(10 * time.Millisecond)
	if sender.count() != 0 {
		t.Fatal("one offline sample armed a duplicate announcement")
	}

	set(false, 6)
	time.Sleep(10 * time.Millisecond)
	set(false, 7)
	time.Sleep(10 * time.Millisecond)
	set(true, 8)
	time.Sleep(10 * time.Millisecond)
	set(true, 9)
	time.Sleep(10 * time.Millisecond)
	if sender.count() != 1 {
		t.Fatalf("new stream announcements=%d, want 1", sender.count())
	}

	set(true, 10)
	time.Sleep(10 * time.Millisecond)
	set(true, 11)
	time.Sleep(10 * time.Millisecond)
	if sender.count() != 1 {
		t.Fatal("same stream was announced twice")
	}
}
