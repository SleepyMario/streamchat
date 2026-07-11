package aggregate

import (
	"context"
	"github.com/SleepyMario/streamchat/internal/chat"
	"testing"
	"time"
)

func msg(id string, p chat.Platform, ts time.Time) chat.Message {
	return chat.Message{ID: id, Platform: p, Timestamp: ts, EventType: chat.EventMessage}
}
func TestOrderingDuplicatesAndBound(t *testing.T) {
	a, _ := New(Config{QueueSize: 4, DuplicateCapacity: 2, ReorderWindow: time.Millisecond})
	c := make(chan chat.Message, 4)
	now := time.Now()
	c <- msg("z", chat.PlatformYouTube, now)
	c <- msg("a", chat.PlatformKick, now)
	c <- msg("z", chat.PlatformYouTube, now)
	close(c)
	out, _ := a.Run(context.Background(), c)
	var got []chat.Message
	for m := range out {
		got = append(got, m)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "z" {
		t.Fatalf("unexpected order %#v", got)
	}
	if a.DuplicateCount() > 2 {
		t.Fatal("duplicate cache exceeded bound")
	}
}
func TestCancellation(t *testing.T) {
	a, _ := New(Config{QueueSize: 1, DuplicateCapacity: 1})
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan chat.Message)
	out, _ := a.Run(ctx, c)
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("unexpected message")
		}
	case <-time.After(time.Second):
		t.Fatal("did not stop")
	}
}
