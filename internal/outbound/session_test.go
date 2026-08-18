package outbound

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordingSender struct{ messages []string }

func (s *recordingSender) Send(_ context.Context, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

type recordingControl struct {
	arguments []string
	result    string
}

func (c *recordingControl) Execute(_ context.Context, argument string) (string, error) {
	c.arguments = append(c.arguments, argument)
	return c.result, nil
}

func TestKickSelectionAndSending(t *testing.T) {
	kick := &recordingSender{}
	s := New(map[string]Sender{"kick": kick})
	if err := s.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	if s.Selected() != "kick" || len(kick.messages) != 0 {
		t.Fatalf("selected=%q messages=%v", s.Selected(), kick.messages)
	}
	if err := s.Handle(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Handle(context.Background(), "/kick how are you"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kick.messages, []string{"hello", "how are you"}) {
		t.Fatalf("messages=%v", kick.messages)
	}
}

func TestTwitchSelectionSendingAndTargetSwitching(t *testing.T) {
	kick := &recordingSender{}
	twitch := &recordingSender{}
	s := NewTargets(
		Target{Name: "kick", Aliases: []string{"kick"}, Sender: kick},
		Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: twitch},
	)
	for _, line := range []string{"/twitch", "hello", "/twitch second", "/kick kick-message", "/twitch final"} {
		if err := s.Handle(context.Background(), line); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	if s.Selected() != "twitch" || !reflect.DeepEqual(twitch.messages, []string{"hello", "second", "final"}) || !reflect.DeepEqual(kick.messages, []string{"kick-message"}) {
		t.Fatalf("selected=%q twitch=%v kick=%v", s.Selected(), twitch.messages, kick.messages)
	}
}

func TestTargetCommandSelectsCanonicalNameAndPersistsOnlyOnChange(t *testing.T) {
	kick := &recordingSender{}
	s := NewTargets(Target{Name: "kick", Aliases: []string{"kick"}, Sender: kick})
	var changes []string
	s.SetSelectionChanged(func(target string) { changes = append(changes, target) })
	if err := s.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	if err := s.Handle(context.Background(), "/kick hello"); err != nil {
		t.Fatal(err)
	}
	if s.Selected() != "kick" || !reflect.DeepEqual(changes, []string{"kick"}) || !reflect.DeepEqual(kick.messages, []string{"hello"}) {
		t.Fatalf("selected=%q changes=%v messages=%v", s.Selected(), changes, kick.messages)
	}
	withoutSelection := NewTargets(Target{Name: "kick", Aliases: []string{"kick"}, Sender: kick})
	if _, err := withoutSelection.Process(context.Background(), "/kk"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("retired alias unexpectedly selected a target: %v", err)
	}
}

func TestRestoreRequiresRegisteredAvailableCanonicalTarget(t *testing.T) {
	kick := &recordingSender{}
	available := NewTargets(Target{Name: "kick", Aliases: []string{"kick"}, Sender: kick})
	if !available.Restore("KICK") || available.Selected() != "kick" {
		t.Fatalf("available restore selected=%q", available.Selected())
	}
	if available.Restore("youtube") {
		t.Fatal("unregistered target restored")
	}
	unavailable := NewTargets(Target{Name: "kick", Aliases: []string{"kick"}, Sender: kick, Unavailable: true})
	if unavailable.Restore("kick") || unavailable.Selected() != "" {
		t.Fatalf("unavailable restore selected=%q", unavailable.Selected())
	}
	if err := unavailable.Handle(context.Background(), "/kick"); err == nil || unavailable.Selected() != "" {
		t.Fatalf("unavailable command err=%v selected=%q", err, unavailable.Selected())
	}
}

func TestPlainTextRequiresTarget(t *testing.T) {
	s := New(map[string]Sender{"kick": &recordingSender{}})
	if err := s.Handle(context.Background(), "hello"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectionIsSessionLocalAndSwitchable(t *testing.T) {
	kick := &recordingSender{}
	youtube := &recordingSender{}
	first := New(map[string]Sender{"kick": kick, "/yt": youtube})
	if err := first.Handle(context.Background(), "/kick first"); err != nil {
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
	second := New(map[string]Sender{"kick": kick})
	if second.Selected() != "" {
		t.Fatalf("new session inherited target %q", second.Selected())
	}
	if err := second.Handle(context.Background(), "not sent"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err=%v", err)
	}
}

func TestKickControlsParseArgumentsAndNeverBecomeChat(t *testing.T) {
	kick := &recordingSender{}
	title := &recordingControl{result: "title result"}
	category := &recordingControl{result: "category result"}
	s := New(map[string]Sender{"kick": kick})
	s.RegisterControl("title", title)
	s.RegisterControl("/category", category)
	if err := s.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Process(context.Background(), "/title New title")
	if err != nil || result != "title result" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	result, err = s.Process(context.Background(), "/title")
	if err != nil || result != "title result" {
		t.Fatalf("empty title result=%q err=%v", result, err)
	}
	result, err = s.Process(context.Background(), "/category Just Chatting")
	if err != nil || result != "category result" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if !reflect.DeepEqual(title.arguments, []string{"New title", ""}) {
		t.Fatalf("title arguments=%v", title.arguments)
	}
	if !reflect.DeepEqual(category.arguments, []string{"Just Chatting"}) {
		t.Fatalf("category arguments=%v", category.arguments)
	}
	if len(kick.messages) != 0 || s.Selected() != "kick" {
		t.Fatalf("controls reached chat or changed target: messages=%v selected=%q", kick.messages, s.Selected())
	}
	if err = s.Handle(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kick.messages, []string{"hello"}) {
		t.Fatalf("existing /kick behavior changed: %v", kick.messages)
	}
}

func TestTargetControlsDispatchThroughSelectedProvider(t *testing.T) {
	kickSender := &recordingSender{}
	twitchSender := &recordingSender{}
	kickTitle := &recordingControl{result: "kick title"}
	twitchTitle := &recordingControl{result: "twitch title"}
	s := NewTargets(
		Target{Name: "kick", Aliases: []string{"kick"}, Sender: kickSender},
		Target{Name: "twitch", Aliases: []string{"twitch"}, Sender: twitchSender},
	)
	s.RegisterTargetControl("title", "kick", kickTitle)
	s.RegisterTargetControl("title", "twitch", twitchTitle)
	if _, err := s.Process(context.Background(), "/title no target"); err == nil || !strings.Contains(err.Error(), "select /kick or /twitch") {
		t.Fatalf("missing-target error=%v", err)
	}
	if err := s.Handle(context.Background(), "/twitch"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Process(context.Background(), "/title Twitch title")
	if err != nil || result != "twitch title" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if err = s.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	result, err = s.Process(context.Background(), "/title Kick title")
	if err != nil || result != "kick title" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if !reflect.DeepEqual(twitchTitle.arguments, []string{"Twitch title"}) || !reflect.DeepEqual(kickTitle.arguments, []string{"Kick title"}) {
		t.Fatalf("twitch=%v kick=%v", twitchTitle.arguments, kickTitle.arguments)
	}
}

func TestSelectionChangeSupportsPersistenceAndStatusObservers(t *testing.T) {
	s := NewTargets(Target{Name: "kick", Aliases: []string{"kick"}, Sender: &recordingSender{}})
	var persisted, status []string
	s.SetSelectionChanged(func(target string) { persisted = append(persisted, target) })
	s.AddSelectionChanged(func(target string) { status = append(status, target) })
	if err := s.Handle(context.Background(), "/kick"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, []string{"kick"}) || !reflect.DeepEqual(status, []string{"kick"}) {
		t.Fatalf("persisted=%v status=%v", persisted, status)
	}
}
