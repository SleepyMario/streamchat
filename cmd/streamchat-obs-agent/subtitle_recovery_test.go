package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCaptureStall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watchCapture(ctx, make(chan struct{}), 20*time.Millisecond); err == nil {
		t.Fatal("stalled capture not detected")
	}
}
func TestSilentFramesKeepCaptureAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	progress := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- watchCapture(ctx, progress, 100*time.Millisecond) }()
	for i := 0; i < 10; i++ {
		progress <- struct{}{}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("silent frames treated as stall: %v", err)
	}
}
func TestSubtitleReconnectWithoutNewWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := retrySubtitleSender(ctx, time.Millisecond, func() error {
		calls++
		if calls == 3 {
			cancel()
		}
		return errors.New("USB reset")
	}, func(string, string) {})
	if calls != 3 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
