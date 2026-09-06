package inputtrigger

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

func TestWatcherRunsOnceForEachNewSession(t *testing.T) {
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
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (Watcher{State: get, OnOnline: func(context.Context) error { calls.Add(1); return nil }, PollInterval: time.Millisecond}).Run(ctx)

	set(true, 1)
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("restart during an existing stream ran the action")
	}
	set(false, 2)
	time.Sleep(10 * time.Millisecond)
	set(true, 3)
	time.Sleep(10 * time.Millisecond)
	set(true, 4)
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("first new session calls=%d", calls.Load())
	}
	set(false, 5)
	time.Sleep(10 * time.Millisecond)
	set(true, 6)
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("second new session calls=%d", calls.Load())
	}
}

func TestWatcherRetriesFailedOnlineAction(t *testing.T) {
	var mu sync.RWMutex
	state := streamprobe.State{}
	get := func() streamprobe.State {
		mu.RLock()
		defer mu.RUnlock()
		return state
	}
	set := func(online bool, n int) {
		mu.Lock()
		state = streamprobe.State{Online: online, CheckedAt: time.Unix(int64(n), 0)}
		mu.Unlock()
	}
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (Watcher{
		State: get,
		OnOnline: func(context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("temporary failure")
			}
			return nil
		},
		PollInterval: time.Millisecond,
		RetryDelay:   time.Millisecond,
	}).Run(ctx)

	set(false, 1)
	time.Sleep(10 * time.Millisecond)
	set(true, 2)
	time.Sleep(10 * time.Millisecond)
	set(true, 3)
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("retry calls=%d", calls.Load())
	}
}
