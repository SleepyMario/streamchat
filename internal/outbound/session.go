package outbound

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var (
	ErrNoTarget          = errors.New("no outbound target selected")
	ErrShutdownRequested = errors.New("interactive shutdown requested")
)

type ControlError struct{ Err error }

func (e *ControlError) Error() string { return e.Err.Error() }
func (e *ControlError) Unwrap() error { return e.Err }

const NoTargetInstruction = "No outbound target selected. Use /kick, /twitch, or /youtube first."

// Sender is the minimal contract shared by outbound chat providers.
type Sender interface {
	Send(context.Context, string) error
}

// SentMessage is the provider-confirmed identity needed to present a
// successful outbound chat message before its asynchronous inbound event
// arrives. The provider message ID lets the inbound event replace the local
// presentation without producing a duplicate.
type SentMessage struct {
	ID, Target, AuthorID, AuthorDisplayName, Text string
}

// ReceiptSender extends Sender for providers whose send API returns the
// provider message ID. Session uses this only for local presentation; the
// provider's inbound event remains authoritative for archival metadata.
type ReceiptSender interface {
	SendMessage(context.Context, string) (SentMessage, error)
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
	targetControls   map[string]map[string]Control
	selected         string
	selectionChanged []func(string)
	sentMu           sync.RWMutex
	sent             func(SentMessage)
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
	s := &Session{targets: make(map[string]registeredTarget), canonical: make(map[string]registeredTarget), controls: make(map[string]Control), targetControls: make(map[string]map[string]Control)}
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

// RegisterTargetControl dispatches a shared command through the currently
// selected provider without introducing provider-specific command syntax.
func (s *Session) RegisterTargetControl(command, target string, control Control) {
	command = strings.TrimPrefix(command, "/")
	target = normalizeTarget(target)
	if command == "" || target == "" || control == nil {
		return
	}
	if s.targetControls[command] == nil {
		s.targetControls[command] = make(map[string]Control)
	}
	s.targetControls[command][target] = control
}

// Handle selects a registered slash-command target and optionally sends the
// rest of that line. Plain text is sent to the most recently selected target.
func (s *Session) Handle(ctx context.Context, line string) error {
	_, err := s.Process(ctx, line)
	return err
}

// Process returns textual local output from a control command. Receipt-capable
// chat sends notify the presentation observer separately so they use the
// normal chat renderer rather than being printed as control output.
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
		if controls, ok := s.targetControls[command]; ok {
			if s.selected == "" {
				return "", &ControlError{Err: errors.New("select /kick, /twitch, or /youtube before /" + command)}
			}
			control, available := controls[s.selected]
			if !available {
				return "", &ControlError{Err: errors.New("/" + command + " is unavailable for the selected target")}
			}
			result, err := control.Execute(ctx, message)
			if err != nil {
				return "", &ControlError{Err: err}
			}
			return result, nil
		}
		if target, ok := s.targets[normalizeTarget(command)]; ok {
			if target.unavailable {
				return "", errors.New(target.name + " outbound target is not configured/available")
			}
			s.selectTarget(target.name)
			if message == "" {
				return "", nil
			}
			return "", s.send(ctx, target, message)
		}
	}
	if s.selected == "" {
		return "", ErrNoTarget
	}
	return "", s.send(ctx, s.canonical[s.selected], line)
}

func (s *Session) send(ctx context.Context, target registeredTarget, message string) error {
	receiptSender, ok := target.sender.(ReceiptSender)
	if !ok {
		return target.sender.Send(ctx, message)
	}
	receipt, err := receiptSender.SendMessage(ctx, message)
	if err != nil {
		return err
	}
	receipt.Target = target.name
	receipt.Text = message
	s.sentMu.RLock()
	observer := s.sent
	s.sentMu.RUnlock()
	if receipt.ID != "" && observer != nil {
		observer(receipt)
	}
	return nil
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
	s.selectionChanged = nil
	if callback != nil {
		s.selectionChanged = append(s.selectionChanged, callback)
	}
}

func (s *Session) AddSelectionChanged(callback func(string)) {
	if callback != nil {
		s.selectionChanged = append(s.selectionChanged, callback)
	}
}

func (s *Session) SetSentObserver(callback func(SentMessage)) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	s.sent = callback
}

func (s *Session) selectTarget(name string) {
	if name == s.selected {
		return
	}
	s.selected = name
	for _, callback := range s.selectionChanged {
		callback(name)
	}
}

func normalizeTarget(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "/")))
}
