package outbound

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingSender struct{ messages []string }

func (s *recordingSender) Send(_ context.Context, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestKickSelectionAndSending(t *testing.T) {
	kick := &recordingSender{}
	s := New(map[string]Sender{"kk": kick})
	if err := s.Handle(context.Background(), "/kk"); err != nil {
		t.Fatal(err)
	}
	if s.Selected() != "kk" || len(kick.messages) != 0 {
		t.Fatalf("selected=%q messages=%v", s.Selected(), kick.messages)
	}
	if err := s.Handle(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Handle(context.Background(), "/kk how are you"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kick.messages, []string{"hello", "how are you"}) {
		t.Fatalf("messages=%v", kick.messages)
	}
}

func TestPlainTextRequiresTarget(t *testing.T) {
	s := New(map[string]Sender{"kk": &recordingSender{}})
	if err := s.Handle(context.Background(), "hello"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectionIsSessionLocalAndSwitchable(t *testing.T) {
	kick := &recordingSender{}
	youtube := &recordingSender{}
	first := New(map[string]Sender{"kk": kick, "/yt": youtube})
	if err := first.Handle(context.Background(), "/kk first"); err != nil {
		t.Fatal(err)
	}
	if err := first.Handle(context.Background(), "/yt second"); err != nil {
		t.Fatal(err)
	}
	if err := first.Handle(context.Background(), "third"); err != nil {
		t.Fatal(err)
	}
	if first.Selected() != "yt" || !reflect.DeepEqual(kick.messages, []string{"first"}) || !reflect.DeepEqual(youtube.messages, []string{"second", "third"}) {
		t.Fatalf("selected=%q kick=%v youtube=%v", first.Selected(), kick.messages, youtube.messages)
	}
	second := New(map[string]Sender{"kk": kick})
	if second.Selected() != "" {
		t.Fatalf("new session inherited target %q", second.Selected())
	}
	if err := second.Handle(context.Background(), "not sent"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err=%v", err)
	}
}
