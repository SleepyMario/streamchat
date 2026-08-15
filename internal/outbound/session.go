package outbound

import (
	"context"
	"errors"
	"strings"
)

var (
	ErrNoTarget          = errors.New("no outbound target selected")
	ErrShutdownRequested = errors.New("interactive shutdown requested")
)

type ControlError struct{ Err error }

func (e *ControlError) Error() string { return e.Err.Error() }
func (e *ControlError) Unwrap() error { return e.Err }

const NoTargetInstruction = "No outbound target selected. Use /kick first."

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

// Target separates a stable provider name from the command aliases that select
// it. Canonical names are suitable for persisted client state.
type Target struct {
	Name        string
	Aliases     []string
	Sender      Sender
	Unavailable bool
}

type registeredTarget struct {
	name        string
	sender      Sender
	unavailable bool
}

// Session tracks the selected outbound target for one interactive run.
type Session struct {
	targets          map[string]registeredTarget
	canonical        map[string]registeredTarget
	controls         map[string]Control
	selected         string
	selectionChanged func(string)
}

func New(targets map[string]Sender) *Session {
	registered := make([]Target, 0, len(targets))
	for command, sender := range targets {
		name := normalizeTarget(command)
		registered = append(registered, Target{Name: name, Aliases: []string{command}, Sender: sender})
	}
	return NewTargets(registered...)
}

func NewTargets(targets ...Target) *Session {
	s := &Session{targets: make(map[string]registeredTarget), canonical: make(map[string]registeredTarget), controls: make(map[string]Control)}
	for _, target := range targets {
		name := normalizeTarget(target.Name)
		if name == "" || target.Sender == nil {
			continue
		}
		entry := registeredTarget{name: name, sender: target.Sender, unavailable: target.Unavailable}
		s.canonical[name] = entry
		for _, alias := range target.Aliases {
			if alias = normalizeTarget(alias); alias != "" {
				s.targets[alias] = entry
			}
		}
	}
	return s
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
				if errors.Is(err, ErrShutdownRequested) {
					return "", err
				}
				return "", &ControlError{Err: err}
			}
			return result, nil
		}
		if target, ok := s.targets[normalizeTarget(command)]; ok {
			s.selectTarget(target.name)
			if message == "" {
				return "", nil
			}
			return "", target.sender.Send(ctx, message)
		}
	}
	if s.selected == "" {
		return "", ErrNoTarget
	}
	return "", s.canonical[s.selected].sender.Send(ctx, line)
}

func (s *Session) Selected() string { return s.selected }

// Restore selects a canonical target only when it is registered and available
// in this client session. It intentionally does not fire the persistence hook.
func (s *Session) Restore(name string) bool {
	name = normalizeTarget(name)
	target, ok := s.canonical[name]
	if !ok || target.unavailable {
		return false
	}
	s.selected = name
	return true
}

func (s *Session) SetSelectionChanged(callback func(string)) {
	s.selectionChanged = callback
}

func (s *Session) selectTarget(name string) {
	if name == s.selected {
		return
	}
	s.selected = name
	if s.selectionChanged != nil {
		s.selectionChanged(name)
	}
}

func normalizeTarget(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "/")))
}
