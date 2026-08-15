package emote

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ueberzugStartupTimeout = time.Second
	ueberzugSocketTimeout  = 500 * time.Millisecond
)

type UeberzugBackend struct {
	mu           sync.RWMutex
	available    bool
	closed       bool
	invalidation func()
	diagnostic   func(error)
	updates      chan []Placement
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	socket       string
	temporaryDir string
	pid          int
	debug        bool
	trace        func(string)
	stderrDone   <-chan struct{}
	confirmed    map[string]Placement
	sway         interface {
		Correct(pid int, identifier string, expected int, requireNew bool) error
		MoveRows(pid int, identifier string, rows int) (bool, error)
		Forget(identifier string)
	}
}

type UeberzugBackendOptions struct {
	TerminalOutput io.Writer
	Debug          bool
	Trace          func(string)
}

func DetectUeberzug() (string, bool) {
	return detectUeberzug(exec.LookPath, os.Getenv, runtime.GOOS)
}

func detectUeberzug(lookPath func(string) (string, error), getenv func(string) string, goos string) (string, bool) {
	if goos != "linux" || getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return "", false
	}
	path, err := lookPath("ueberzug")
	return path, err == nil && path != ""
}

func NewUeberzugBackend(terminalOutput io.Writer) (*UeberzugBackend, error) {
	return newUeberzugBackend(UeberzugBackendOptions{TerminalOutput: terminalOutput})
}

func newUeberzugBackend(options UeberzugBackendOptions) (*UeberzugBackend, error) {
	executable, ok := DetectUeberzug()
	if !ok {
		return nil, errors.New("Überzug++ is unavailable")
	}
	temporaryDir, err := os.MkdirTemp("", "streamchat-ueberzug-")
	if err != nil {
		return nil, errors.New("create Überzug++ runtime directory")
	}
	pidFile := filepath.Join(temporaryDir, "pid")
	output := ueberzugOutput(os.Getenv)
	sway, err := newSwayCorrector(output, os.Getenv, options.Trace)
	if err != nil {
		_ = os.RemoveAll(temporaryDir)
		return nil, errors.New("prepare Sway overlay placement")
	}
	arguments := ueberzugArguments(pidFile, output, options.Debug)
	command := exec.Command(executable, arguments...)
	command.Env = replaceEnvironment(os.Environ(), "UEBERZUGPP_TMPDIR", temporaryDir)
	command.Stdin = nil
	if options.TerminalOutput == nil {
		options.TerminalOutput = os.Stdout
	}
	command.Stdout = options.TerminalOutput
	var stderrRead, stderrWrite *os.File
	var stderrDone chan struct{}
	if options.Debug {
		stderrRead, stderrWrite, err = os.Pipe()
		if err != nil {
			_ = os.RemoveAll(temporaryDir)
			return nil, errors.New("capture Überzug++ stderr")
		}
		command.Stderr = stderrWrite
		stderrDone = make(chan struct{})
		go captureUeberzugStderr(stderrRead, options.Trace, stderrDone)
	} else {
		command.Stderr = io.Discard
	}
	traceUeberzug(options.Trace, "helper startup: executable=%s output=%s", filepath.Base(executable), ueberzugOutput(os.Getenv))
	if err = command.Start(); err != nil {
		if stderrWrite != nil {
			_ = stderrWrite.Close()
		}
		waitUeberzugStderr(stderrDone)
		_ = os.RemoveAll(temporaryDir)
		return nil, errors.New("start Überzug++")
	}
	if stderrWrite != nil {
		_ = stderrWrite.Close()
	}
	launcherDone := make(chan error, 1)
	go func() {
		launcherDone <- command.Wait()
		close(launcherDone)
	}()
	pid, socket, err := awaitUeberzugSocket(pidFile, temporaryDir, launcherDone)
	if err != nil {
		if pid > 0 {
			stopDaemon(pid, socket)
		}
		waitUeberzugStderr(stderrDone)
		captureUeberzugLog(temporaryDir, options.Trace)
		_ = os.RemoveAll(temporaryDir)
		return nil, err
	}
	traceUeberzug(options.Trace, "helper ready: pid=%d socket=%s", pid, filepath.Base(socket))
	backend := &UeberzugBackend{available: true, updates: make(chan []Placement, 1), stop: make(chan struct{}), done: make(chan struct{}), socket: socket, temporaryDir: temporaryDir, pid: pid, debug: options.Debug, trace: options.Trace, stderrDone: stderrDone, confirmed: make(map[string]Placement), sway: sway}
	go backend.run()
	return backend, nil
}

func ueberzugArguments(pidFile, output string, debug bool) []string {
	// Streamchat owns its persistent, content-validated asset cache. Disabling
	// Überzug++'s resized derivative cache prevents an old two-cell preview
	// from being reused after the requested terminal geometry changes.
	arguments := []string{"layer", "--no-stdin", "--no-cache", "--pid-file", pidFile}
	if !debug {
		arguments = append(arguments, "--silent")
	}
	if output != "" {
		arguments = append(arguments, "--output", output)
	}
	return arguments
}

func captureUeberzugStderr(reader *os.File, trace func(string), done chan<- struct{}) {
	defer close(done)
	defer reader.Close()
	scanner := bufio.NewScanner(io.LimitReader(reader, 1<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		traceUeberzug(trace, "helper stderr: %s", sanitizeUeberzugDiagnostic(scanner.Text()))
	}
}

func waitUeberzugStderr(done <-chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

func ueberzugOutput(getenv func(string) string) string {
	if getenv("WAYLAND_DISPLAY") != "" {
		return "wayland"
	}
	terminal := strings.ToLower(getenv("TERM"))
	if strings.Contains(terminal, "kitty") {
		return "kitty"
	}
	if strings.Contains(strings.ToLower(getenv("TERM_PROGRAM")), "iterm") {
		return "iterm2"
	}
	return ""
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func awaitUeberzugSocket(pidFile, directory string, launcherDone <-chan error) (int, string, error) {
	deadline := time.Now().Add(ueberzugStartupTimeout)
	pid := 0
	for time.Now().Before(deadline) {
		if launcherDone != nil {
			select {
			case launchErr := <-launcherDone:
				if launchErr != nil {
					return pid, "", errors.New("Überzug++ launcher failed")
				}
				// --no-stdin deliberately daemonizes. A successful launcher
				// exit is expected; the PID file/socket identify the helper.
				launcherDone = nil
			default:
			}
		}
		data, err := os.ReadFile(pidFile)
		if err == nil {
			parsedPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && parsedPID > 0 {
				pid = parsedPID
				socket := filepath.Join(directory, fmt.Sprintf("ueberzugpp-%d.socket", pid))
				if info, statErr := os.Stat(socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
					return pid, socket, nil
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pid, "", errors.New("Überzug++ control socket did not become ready")
}

func (b *UeberzugBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available && !b.closed
}

func (b *UeberzugBackend) PID() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pid
}

func (b *UeberzugBackend) Confirmed(identifier string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.confirmed[identifier]
	return ok && b.available && !b.closed
}

func (b *UeberzugBackend) clearPID() {
	b.mu.Lock()
	b.pid = 0
	b.mu.Unlock()
}

func (b *UeberzugBackend) SetInvalidation(invalidation func()) {
	b.mu.Lock()
	b.invalidation = invalidation
	b.mu.Unlock()
}

func (b *UeberzugBackend) SetDiagnostic(diagnostic func(error)) {
	b.mu.Lock()
	b.diagnostic = diagnostic
	b.mu.Unlock()
}

func (b *UeberzugBackend) Update(placements []Placement) {
	b.mu.RLock()
	available := b.available && !b.closed
	b.mu.RUnlock()
	if !available {
		return
	}
	placements = append([]Placement(nil), placements...)
	select {
	case b.updates <- placements:
	default:
		select {
		case <-b.updates:
		default:
		}
		select {
		case b.updates <- placements:
		default:
		}
	}
}

func (b *UeberzugBackend) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.available = false
		b.mu.Unlock()
		close(b.stop)
		<-b.done
	})
	return nil
}

func (b *UeberzugBackend) run() {
	defer close(b.done)
	active := make([]Placement, 0)
	monitor := time.NewTicker(250 * time.Millisecond)
	defer monitor.Stop()
	monitorC := monitor.C
	for {
		select {
		case placements := <-b.updates:
			if err := b.sendUpdate(active, placements); err != nil {
				b.tracef("socket error: %s", err)
				stopDaemon(b.PID(), b.socket)
				waitUeberzugStderr(b.stderrDone)
				b.captureFailureEvidence()
				b.fail(err)
				b.clearPID()
				_ = os.RemoveAll(b.temporaryDir)
				return
			}
			active = placements
			b.setConfirmed(placements)
		case <-monitorC:
			if !processAlive(b.PID()) {
				pid := b.PID()
				b.tracef("process disappearance: pid=%d", pid)
				waitUeberzugStderr(b.stderrDone)
				b.captureFailureEvidence()
				b.fail(errors.New("Überzug++ helper exited"))
				b.clearPID()
				_ = os.RemoveAll(b.temporaryDir)
				return
			}
		case <-b.stop:
			_ = sendUeberzugUpdateWithTrace(b.socket, active, nil, b.trace)
			b.tracef("helper shutdown requested: pid=%d", b.PID())
			stopDaemon(b.PID(), b.socket)
			waitUeberzugStderr(b.stderrDone)
			b.clearPID()
			_ = os.RemoveAll(b.temporaryDir)
			return
		}
	}
}

// Überzug++ 2.9's Wayland backend creates one XDG surface per add. On Sway,
// submitting several adds in one burst can overlap its surface/config IPC and
// corrupt a JSON response. Multi-output placement therefore submits and
// verifies one surface at a time. Other backends retain the compact batch.
func (b *UeberzugBackend) sendUpdate(old, next []Placement) error {
	if b.sway == nil {
		return sendUeberzugUpdateWithTrace(b.socket, old, next, b.trace)
	}
	removed, updated := placementChanges(old, next)
	working := make(map[string]Placement, len(old)+len(updated))
	for _, placement := range old {
		if validPlacement(placement) {
			working[placement.Identifier] = placement
		}
	}
	for _, placement := range removed {
		if err := sendUeberzugUpdateWithTrace(b.socket, []Placement{placement}, nil, b.trace); err != nil {
			return err
		}
		b.sway.Forget(placement.Identifier)
		delete(working, placement.Identifier)
	}
	for _, placement := range updated {
		if previous, exists := working[placement.Identifier]; exists && rowOnlyPlacementChange(previous, placement) {
			moved, err := b.sway.MoveRows(b.PID(), placement.Identifier, placement.Y-previous.Y)
			if err != nil {
				return err
			}
			if moved {
				working[placement.Identifier] = placement
				continue
			}
		}
		if err := sendUeberzugUpdateWithTrace(b.socket, nil, []Placement{placement}, b.trace); err != nil {
			return err
		}
		working[placement.Identifier] = placement
		if err := b.sway.Correct(b.PID(), placement.Identifier, len(working), true); err != nil {
			b.tracef("Sway correction error: %s", err)
			return errors.New("correct Überzug++ Sway placement")
		}
	}
	return nil
}

func rowOnlyPlacementChange(previous, next Placement) bool {
	return previous.Identifier == next.Identifier &&
		previous.Path == next.Path &&
		previous.X == next.X &&
		previous.Width == next.Width &&
		previous.Height == next.Height &&
		previous.Y != next.Y
}

func (b *UeberzugBackend) setConfirmed(placements []Placement) {
	next := make(map[string]Placement, len(placements))
	for _, placement := range placements {
		if validPlacement(placement) {
			next[placement.Identifier] = placement
		}
	}
	b.mu.Lock()
	changed := len(next) != len(b.confirmed)
	if !changed {
		for identifier, placement := range next {
			if b.confirmed[identifier] != placement {
				changed = true
				break
			}
		}
	}
	b.confirmed = next
	invalidation := b.invalidation
	b.mu.Unlock()
	if changed && invalidation != nil {
		invalidation()
	}
}

func (b *UeberzugBackend) tracef(format string, values ...any) {
	traceUeberzug(b.trace, format, values...)
}

func (b *UeberzugBackend) captureFailureEvidence() {
	if !b.debug {
		return
	}
	captureUeberzugLog(b.temporaryDir, b.trace)
}

func (b *UeberzugBackend) fail(err error) {
	b.mu.Lock()
	wasAvailable := b.available
	b.available = false
	b.confirmed = make(map[string]Placement)
	invalidation := b.invalidation
	diagnostic := b.diagnostic
	b.mu.Unlock()
	if wasAvailable && diagnostic != nil {
		diagnostic(err)
	}
	if wasAvailable && invalidation != nil {
		invalidation()
	}
}

func sendUeberzugUpdate(socket string, old, next []Placement) error {
	return sendUeberzugUpdateWithTrace(socket, old, next, nil)
}

func sendUeberzugUpdateWithTrace(socket string, old, next []Placement, trace func(string)) error {
	removed, updated := placementChanges(old, next)
	if len(removed) == 0 && len(updated) == 0 {
		return nil
	}
	connection, err := net.DialTimeout("unix", socket, ueberzugSocketTimeout)
	if err != nil {
		return errors.New("connect to Überzug++ control socket")
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(time.Now().Add(ueberzugSocketTimeout))
	encoder := json.NewEncoder(connection)
	for _, placement := range removed {
		traceUeberzugPlacement(trace, "remove", placement)
		if err = encoder.Encode(map[string]any{"action": "remove", "identifier": placement.Identifier}); err != nil {
			return errors.New("remove Überzug++ image")
		}
	}
	for _, placement := range updated {
		if placement.Identifier == "" || placement.Path == "" || placement.Width < 1 || placement.Height < 1 {
			continue
		}
		traceUeberzugPlacement(trace, "add", placement)
		command := map[string]any{"action": "add", "identifier": placement.Identifier, "path": placement.Path, "x": max(placement.X, 0), "y": max(placement.Y, 0), "max_width": placement.Width, "max_height": placement.Height}
		if err = encoder.Encode(command); err != nil {
			return errors.New("add Überzug++ image")
		}
	}
	return nil
}

func traceUeberzugPlacement(trace func(string), action string, placement Placement) {
	if trace == nil {
		return
	}
	traceUeberzug(trace, "socket command: action=%s identifier=%s cache=%s x=%d y=%d max_width=%d max_height=%d", action, sanitizeUeberzugIdentifier(placement.Identifier), sanitizedCachePath(placement.Path), max(placement.X, 0), max(placement.Y, 0), placement.Width, placement.Height)
}

func traceUeberzug(trace func(string), format string, values ...any) {
	if trace != nil {
		trace(sanitizeUeberzugDiagnostic(fmt.Sprintf(format, values...)))
	}
}

func sanitizeUeberzugIdentifier(value string) string {
	var result strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			result.WriteRune(r)
		}
	}
	if result.Len() == 0 {
		return "invalid"
	}
	return result.String()
}

func sanitizedCachePath(path string) string {
	base := filepath.Base(path)
	provider := filepath.Base(filepath.Dir(path))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSuffix(stem, ".static")
	if !safeKey(stem) || !safeKey(provider) {
		return "invalid"
	}
	return provider + "/" + base
}

func sanitizeUeberzugDiagnostic(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		if r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		value = value[:2048] + "…"
	}
	return value
}

func captureUeberzugLog(directory string, trace func(string)) {
	if trace == nil {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(directory, "ueberzugpp-*.log"))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(data) > 64<<10 {
			data = data[len(data)-(64<<10):]
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = sanitizeUeberzugDiagnostic(line)
			if line != "" {
				traceUeberzug(trace, "helper log: %s", line)
			}
		}
	}
}

// placementChanges preserves overlays that are still valid. Überzug++'s
// Wayland canvas replaces an existing identifier on add, so moved or changed
// overlays can be updated in place without first exposing their text backing.
func placementChanges(old, next []Placement) (removed, updated []Placement) {
	oldByID := make(map[string]Placement, len(old))
	nextByID := make(map[string]Placement, len(next))
	removedIDs := make(map[string]struct{}, len(old))
	updatedIDs := make(map[string]struct{}, len(next))
	for _, placement := range old {
		if placement.Identifier != "" {
			oldByID[placement.Identifier] = placement
		}
	}
	for _, placement := range next {
		if validPlacement(placement) {
			nextByID[placement.Identifier] = placement
		}
	}
	for _, placement := range old {
		if !validPlacement(placement) {
			continue
		}
		if _, exists := nextByID[placement.Identifier]; !exists {
			if _, seen := removedIDs[placement.Identifier]; !seen {
				removed = append(removed, placement)
				removedIDs[placement.Identifier] = struct{}{}
			}
			delete(oldByID, placement.Identifier)
		}
	}
	for _, placement := range next {
		if !validPlacement(placement) {
			continue
		}
		previous, exists := oldByID[placement.Identifier]
		_, seen := updatedIDs[placement.Identifier]
		if !seen && (!exists || previous != placement) {
			updated = append(updated, placement)
			updatedIDs[placement.Identifier] = struct{}{}
		}
		delete(oldByID, placement.Identifier)
	}
	return removed, updated
}

func validPlacement(placement Placement) bool {
	return placement.Identifier != "" && placement.Path != "" && placement.Width > 0 && placement.Height > 0
}

func sendUeberzugExit(socket string) error {
	connection, err := net.DialTimeout("unix", socket, ueberzugSocketTimeout)
	if err != nil {
		return errors.New("connect to Überzug++ control socket")
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(time.Now().Add(ueberzugSocketTimeout))
	if _, err = io.WriteString(connection, "EXIT"); err != nil {
		return errors.New("stop Überzug++ helper")
	}
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAlive(pid)
}

func stopDaemon(pid int, socket string) {
	if pid <= 0 {
		return
	}
	_ = sendUeberzugExit(socket)
	if waitProcessExit(pid, time.Second) {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Signal(syscall.SIGTERM)
	if waitProcessExit(pid, 500*time.Millisecond) {
		return
	}
	_ = process.Signal(syscall.SIGKILL)
	_ = waitProcessExit(pid, 500*time.Millisecond)
}
