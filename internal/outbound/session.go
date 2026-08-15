package outbound

import (
	"context"
	"errors"
	"strings"
)

var ErrNoTarget = errors.New("no outbound target selected")

type ControlError struct{ Err error }

func (e *ControlError) Error() string { return e.Err.Error() }
func (e *ControlError) Unwrap() error { return e.Err }

const NoTargetInstruction = "No outbound target selected. Use /kk first."

// Sender is the minimal contract shared by outbound chat providers.
type Sender interface {
	Send(context.Context, string) error
}

// Control handles a slash command that is independent of chat target state.
type Control interface {
	Execute(context.Context, string) (string, error)
}

type ControlFunc func(context.Context, string) (string, error)

func (f ControlFunc) Execute(ctx context.Context, argument string) (string, error) {
	return f(ctx, argument)
}

// Session tracks the selected outbound target for one interactive run.
type Session struct {
	targets  map[string]Sender
	controls map[string]Control
	selected string
}

func New(targets map[string]Sender) *Session {
	copy := make(map[string]Sender, len(targets))
	for command, sender := range targets {
		copy[strings.TrimPrefix(command, "/")] = sender
	}
	return &Session{targets: copy, controls: make(map[string]Control)}
}

func (s *Session) RegisterControl(command string, control Control) {
	s.controls[strings.TrimPrefix(command, "/")] = control
}

// Handle selects a registered slash-command target and optionally sends the
// rest of that line. Plain text is sent to the most recently selected target.
func (s *Session) Handle(ctx context.Context, line string) error {
	_, err := s.Process(ctx, line)
	return err
}

// Process returns local output from a control command. Chat sends return no
// output because their normal inbound event supplies the visible echo.
func (s *Session) Process(ctx context.Context, line string) (string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	if strings.HasPrefix(line, "/") {
		command, message, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
		message = strings.TrimSpace(message)
		if control, ok := s.controls[command]; ok {
			result, err := control.Execute(ctx, message)
			if err != nil {
				return "", &ControlError{Err: err}
			}
			return result, nil
		}
		if sender, ok := s.targets[command]; ok {
			s.selected = command
			if message == "" {
				return "", nil
			}
			return "", sender.Send(ctx, message)
		}
	}
	if s.selected == "" {
		return "", ErrNoTarget
	}
	return "", s.targets[s.selected].Send(ctx, line)
}

func (s *Session) Selected() string { return s.selected }
