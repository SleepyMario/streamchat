package emote

import (
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
	executable, ok := DetectUeberzug()
	if !ok {
		return nil, errors.New("Überzug++ is unavailable")
	}
	temporaryDir, err := os.MkdirTemp("", "streamchat-ueberzug-")
	if err != nil {
		return nil, errors.New("create Überzug++ runtime directory")
	}
	pidFile := filepath.Join(temporaryDir, "pid")
	arguments := []string{"layer", "--no-stdin", "--silent", "--pid-file", pidFile}
	if output := ueberzugOutput(os.Getenv); output != "" {
		arguments = append(arguments, "--output", output)
	}
	command := exec.Command(executable, arguments...)
	command.Env = replaceEnvironment(os.Environ(), "UEBERZUGPP_TMPDIR", temporaryDir)
	command.Stdin = nil
	if terminalOutput == nil {
		terminalOutput = os.Stdout
	}
	command.Stdout = terminalOutput
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return nil, errors.New("start Überzug++")
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
		_ = os.RemoveAll(temporaryDir)
		return nil, err
	}
	backend := &UeberzugBackend{available: true, updates: make(chan []Placement, 1), stop: make(chan struct{}), done: make(chan struct{}), socket: socket, temporaryDir: temporaryDir, pid: pid}
	go backend.run()
	return backend, nil
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
	for {
		select {
		case placements := <-b.updates:
			if err := sendUeberzugUpdate(b.socket, active, placements); err != nil {
				b.fail(err)
				stopDaemon(b.PID(), b.socket)
				b.clearPID()
				_ = os.RemoveAll(b.temporaryDir)
				continue
			}
			active = placements
		case <-monitor.C:
			if !processAlive(b.PID()) {
				b.fail(errors.New("Überzug++ helper exited"))
				b.clearPID()
				_ = os.RemoveAll(b.temporaryDir)
			}
		case <-b.stop:
			_ = sendUeberzugUpdate(b.socket, active, nil)
			stopDaemon(b.PID(), b.socket)
			b.clearPID()
			_ = os.RemoveAll(b.temporaryDir)
			return
		}
	}
}

func (b *UeberzugBackend) fail(err error) {
	b.mu.Lock()
	wasAvailable := b.available
	b.available = false
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
		if err = encoder.Encode(map[string]any{"action": "remove", "identifier": placement.Identifier}); err != nil {
			return errors.New("remove Überzug++ image")
		}
	}
	for _, placement := range updated {
		if placement.Identifier == "" || placement.Path == "" || placement.Width < 1 || placement.Height < 1 {
			continue
		}
		command := map[string]any{"action": "add", "identifier": placement.Identifier, "path": placement.Path, "x": max(placement.X, 0), "y": max(placement.Y, 0), "max_width": placement.Width, "max_height": placement.Height}
		if err = encoder.Encode(command); err != nil {
			return errors.New("add Überzug++ image")
		}
	}
	return nil
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
