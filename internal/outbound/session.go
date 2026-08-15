package outbound

import (
	"context"
	"errors"
	"strings"
)

var ErrNoTarget = errors.New("no outbound target selected")

const NoTargetInstruction = "No outbound target selected. Use /kk first."

// Sender is the minimal contract shared by outbound chat providers.
type Sender interface {
	Send(context.Context, string) error
}

// Session tracks the selected outbound target for one interactive run.
type Session struct {
	targets  map[string]Sender
	selected string
}

func New(targets map[string]Sender) *Session {
	copy := make(map[string]Sender, len(targets))
	for command, sender := range targets {
		copy[strings.TrimPrefix(command, "/")] = sender
	}
	return &Session{targets: copy}
}

// Handle selects a registered slash-command target and optionally sends the
// rest of that line. Plain text is sent to the most recently selected target.
func (s *Session) Handle(ctx context.Context, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "/") {
		command, message, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
		if sender, ok := s.targets[command]; ok {
			s.selected = command
			message = strings.TrimSpace(message)
			if message == "" {
				return nil
			}
			return sender.Send(ctx, message)
		}
	}
	if s.selected == "" {
		return ErrNoTarget
	}
	return s.targets[s.selected].Send(ctx, line)
}

func (s *Session) Selected() string { return s.selected }
