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
	updates      chan []Placement
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	socket       string
	temporaryDir string
	command      *exec.Cmd
	processDone  chan struct{}
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
	processDone := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(processDone)
	}()
	socket, err := awaitUeberzugSocket(pidFile, temporaryDir, processDone)
	if err != nil {
		stopProcess(command, processDone)
		_ = os.RemoveAll(temporaryDir)
		return nil, err
	}
	backend := &UeberzugBackend{available: true, updates: make(chan []Placement, 1), stop: make(chan struct{}), done: make(chan struct{}), socket: socket, temporaryDir: temporaryDir, command: command, processDone: processDone}
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

func awaitUeberzugSocket(pidFile, directory string, processDone <-chan struct{}) (string, error) {
	deadline := time.Now().Add(ueberzugStartupTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-processDone:
			return "", errors.New("Überzug++ exited during startup")
		default:
		}
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				socket := filepath.Join(directory, fmt.Sprintf("ueberzugpp-%d.socket", pid))
				if info, statErr := os.Stat(socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
					return socket, nil
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", errors.New("Überzug++ control socket did not become ready")
}

func (b *UeberzugBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available && !b.closed
}

func (b *UeberzugBackend) SetInvalidation(invalidation func()) {
	b.mu.Lock()
	b.invalidation = invalidation
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
	for {
		select {
		case placements := <-b.updates:
			if err := sendUeberzugUpdate(b.socket, active, placements); err != nil {
				b.fail()
				stopProcess(b.command, b.processDone)
				b.processDone = nil
				_ = os.RemoveAll(b.temporaryDir)
				continue
			}
			active = placements
		case <-b.processDone:
			b.fail()
			b.processDone = nil
			_ = os.RemoveAll(b.temporaryDir)
		case <-b.stop:
			_ = sendUeberzugUpdate(b.socket, active, nil)
			stopProcess(b.command, b.processDone)
			_ = os.RemoveAll(b.temporaryDir)
			return
		}
	}
}

func (b *UeberzugBackend) fail() {
	b.mu.Lock()
	wasAvailable := b.available
	b.available = false
	invalidation := b.invalidation
	b.mu.Unlock()
	if wasAvailable && invalidation != nil {
		invalidation()
	}
}

func sendUeberzugUpdate(socket string, old, next []Placement) error {
	connection, err := net.DialTimeout("unix", socket, ueberzugSocketTimeout)
	if err != nil {
		return errors.New("connect to Überzug++ control socket")
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(time.Now().Add(ueberzugSocketTimeout))
	encoder := json.NewEncoder(connection)
	for _, placement := range old {
		if err = encoder.Encode(map[string]any{"action": "remove", "identifier": placement.Identifier}); err != nil {
			return errors.New("remove Überzug++ image")
		}
	}
	for _, placement := range next {
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

func stopProcess(command *exec.Cmd, done <-chan struct{}) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Signal(os.Interrupt)
	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	_ = command.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
}
