package emote

import (
	"bufio"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUeberzugDetectionAndBackendSelection(t *testing.T) {
	look := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	env := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "TERM": "xterm-kitty"}
	get := func(key string) string { return env[key] }
	if path, ok := detectUeberzug(look, get, "linux"); !ok || path != "/usr/bin/ueberzug" {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
	if got := ueberzugOutput(get); got != "wayland" {
		t.Fatalf("output=%q", got)
	}
	env["SSH_TTY"] = "/dev/pts/1"
	if _, ok := detectUeberzug(look, get, "linux"); ok {
		t.Fatal("image helper enabled over SSH")
	}
}

func TestUeberzugDebugArgumentsExposeDiagnosticsAndNormalModeIsSilent(t *testing.T) {
	normal := ueberzugArguments("/tmp/pid", "wayland", false)
	debug := ueberzugArguments("/tmp/pid", "wayland", true)
	if !slices.Contains(normal, "--silent") {
		t.Fatalf("normal arguments=%q", normal)
	}
	if !slices.Contains(normal, "--no-cache") || !slices.Contains(debug, "--no-cache") {
		t.Fatalf("Streamchat cache ownership missing: normal=%q debug=%q", normal, debug)
	}
	if slices.Contains(debug, "--silent") {
		t.Fatalf("debug arguments=%q", debug)
	}
}

func TestUeberzugDebugCapturesSanitizedStderr(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var messages []string
	done := make(chan struct{})
	go captureUeberzugStderr(reader, func(message string) {
		mu.Lock()
		messages = append(messages, message)
		mu.Unlock()
	}, done)
	_, _ = io.WriteString(writer, "fatal\x00bad placement\n")
	_ = writer.Close()
	waitUeberzugStderr(done)
	mu.Lock()
	joined := strings.Join(messages, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "helper stderr: fatal bad placement") || strings.ContainsRune(joined, '\x00') {
		t.Fatalf("diagnostics=%q", joined)
	}
}

func TestUeberzugSocketUpdateRemovesOldAndAddsCellPlacement(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []map[string]any, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		decoder := json.NewDecoder(bufio.NewReader(connection))
		var values []map[string]any
		for range 2 {
			var value map[string]any
			if decoder.Decode(&value) != nil {
				return
			}
			values = append(values, value)
		}
		commands <- values
	}()
	old := []Placement{{Identifier: "old", Path: "/old", Width: 2, Height: 1}}
	next := []Placement{{Identifier: "new", Path: "/cache/7.img", X: 12, Y: 5, Width: 2, Height: 1}}
	if err = sendUeberzugUpdate(socket, old, next); err != nil {
		t.Fatal(err)
	}
	got := <-commands
	if got[0]["action"] != "remove" || got[1]["action"] != "add" || got[1]["x"] != float64(12) || got[1]["y"] != float64(5) {
		t.Fatalf("commands=%+v", got)
	}
}

func TestUeberzugSocketUpdatePreservesUnchangedAndRepositionsWithoutRemove(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []map[string]any, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		decoder := json.NewDecoder(bufio.NewReader(connection))
		var values []map[string]any
		for {
			var value map[string]any
			if decoder.Decode(&value) != nil {
				break
			}
			values = append(values, value)
		}
		commands <- values
	}()
	stable := Placement{Identifier: "stable", Path: "/cache/stable.img", X: 3, Y: 4, Width: 2, Height: 1}
	moved := Placement{Identifier: "moved", Path: "/cache/moved.img", X: 7, Y: 5, Width: 2, Height: 1}
	nextMoved := moved
	nextMoved.Y = 4
	if err = sendUeberzugUpdate(socket, []Placement{stable, moved}, []Placement{stable, nextMoved}); err != nil {
		t.Fatal(err)
	}
	got := <-commands
	if len(got) != 1 || got[0]["action"] != "add" || got[0]["identifier"] != "moved" || got[0]["y"] != float64(4) {
		t.Fatalf("commands=%+v", got)
	}
}

func TestUeberzugTraceIdentifiesExactCommandBeforeFailure(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	providerDirectory := filepath.Join(directory, "kick")
	if err = os.Mkdir(providerDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_, _ = io.ReadAll(connection)
			_ = connection.Close()
		}
		close(accepted)
	}()
	var trace string
	placement := Placement{Identifier: "streamchat-message-0", Path: filepath.Join(providerDirectory, "1730756.static.png"), X: 25, Y: 22, Width: 2, Height: 1}
	if err = sendUeberzugUpdateWithTrace(socket, nil, []Placement{placement}, func(message string) { trace = message }); err != nil {
		t.Fatal(err)
	}
	<-accepted
	want := "socket command: action=add identifier=streamchat-message-0 cache=kick/1730756.static.png x=25 y=22 max_width=2 max_height=1"
	if trace != want {
		t.Fatalf("trace=%q want=%q", trace, want)
	}
}

func TestPlacementChangesRemovesOnlyImagesOutsideViewport(t *testing.T) {
	old := []Placement{
		{Identifier: "gone", Path: "/cache/gone.img", Y: 2, Width: 2, Height: 1},
		{Identifier: "visible", Path: "/cache/visible.img", Y: 3, Width: 2, Height: 1},
	}
	next := []Placement{{Identifier: "visible", Path: "/cache/visible.img", Y: 2, Width: 2, Height: 1}}
	removed, updated := placementChanges(old, next)
	if len(removed) != 1 || removed[0].Identifier != "gone" {
		t.Fatalf("removed=%+v", removed)
	}
	if len(updated) != 1 || updated[0].Identifier != "visible" || updated[0].Y != 2 {
		t.Fatalf("updated=%+v", updated)
	}
}

func TestAwaitUeberzugSocketAcceptsExpectedDaemonizingLauncherExit(t *testing.T) {
	directory := t.TempDir()
	pid := 4242
	pidFile := filepath.Join(directory, "pid")
	if err := os.WriteFile(pidFile, []byte("4242"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "ueberzugpp-4242.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	launcherDone := make(chan error, 1)
	launcherDone <- nil
	close(launcherDone)
	gotPID, gotSocket, err := awaitUeberzugSocket(pidFile, directory, launcherDone)
	if err != nil || gotPID != pid || gotSocket != socket {
		t.Fatalf("pid=%d socket=%q err=%v", gotPID, gotSocket, err)
	}
}

func TestUeberzugExitUsesVersion210SocketProtocol(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	payload := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		data, _ := io.ReadAll(connection)
		payload <- string(data)
	}()
	if err = sendUeberzugExit(socket); err != nil {
		t.Fatal(err)
	}
	if got := <-payload; got != "EXIT" {
		t.Fatalf("exit payload=%q", got)
	}
}

func TestStopDaemonUsesSocketExitAndLeavesNoProcess(t *testing.T) {
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_, _ = io.ReadAll(connection)
			_ = connection.Close()
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	stopDaemon(command.Process.Pid, socket)
	_ = listener.Close()
	<-done
	if processAlive(command.Process.Pid) {
		t.Fatal("helper remains alive")
	}
}

func TestUeberzugDaemonDeathReportsProcessAndPrivateLogWithoutLeakingWorker(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ueberzugpp-test.log"), []byte("uncaught worker failure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var messages []string
	diagnostic := make(chan error, 1)
	backend := &UeberzugBackend{
		available:    true,
		updates:      make(chan []Placement, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		temporaryDir: directory,
		pid:          99999999,
		debug:        true,
		confirmed:    make(map[string]Placement),
		trace: func(message string) {
			mu.Lock()
			messages = append(messages, message)
			mu.Unlock()
		},
		diagnostic: func(err error) { diagnostic <- err },
	}
	go backend.run()
	select {
	case err := <-diagnostic:
		if err == nil || !strings.Contains(err.Error(), "helper exited") {
			t.Fatalf("diagnostic=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon disappearance was not detected")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	joined := strings.Join(messages, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "process disappearance: pid=99999999") || !strings.Contains(joined, "helper log: uncaught worker failure") {
		t.Fatalf("diagnostics=%q", joined)
	}
	select {
	case <-backend.done:
	default:
		t.Fatal("backend monitor worker remains alive")
	}
}

type lifecycleBackend struct {
	closed int
}

type recordingSwayCorrector struct {
	expected []int
	moves    []int
}

func (c *recordingSwayCorrector) Correct(_ int, _ string, expected int, requireNew bool) error {
	if !requireNew {
		return errors.New("placement did not require a new surface")
	}
	c.expected = append(c.expected, expected)
	return nil
}

func (c *recordingSwayCorrector) MoveRows(_ int, _ string, rows int) (bool, error) {
	c.moves = append(c.moves, rows)
	return true, nil
}

func (*recordingSwayCorrector) Forget(string) {}

func TestSwayUpdateSubmitsAndConfirmsOneSurfacePerConnection(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []map[string]any, 1)
	go func() {
		values := make([]map[string]any, 0, 2)
		for range 2 {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var value map[string]any
			_ = json.NewDecoder(connection).Decode(&value)
			_ = connection.Close()
			values = append(values, value)
		}
		commands <- values
	}()
	corrector := &recordingSwayCorrector{}
	backend := &UeberzugBackend{socket: socket, pid: 42, sway: corrector}
	next := []Placement{
		{Identifier: "one", Path: "/cache/kick/1.png", Width: 3, Height: 1},
		{Identifier: "two", Path: "/cache/kick/2.png", X: 4, Width: 3, Height: 1},
	}
	if err = backend.sendUpdate(nil, next); err != nil {
		t.Fatal(err)
	}
	got := <-commands
	if len(got) != 2 || got[0]["identifier"] != "one" || got[1]["identifier"] != "two" {
		t.Fatalf("commands=%+v", got)
	}
	if !reflect.DeepEqual(corrector.expected, []int{1, 2}) {
		t.Fatalf("expected surface counts=%v", corrector.expected)
	}
}

func TestSwayRowShiftMovesSurfaceWithoutSendingAnotherAdd(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "control.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	corrector := &recordingSwayCorrector{}
	backend := &UeberzugBackend{socket: socket, pid: 42, sway: corrector}
	previous := Placement{Identifier: "one", Path: "/cache/kick/1.png", X: 4, Y: 8, Width: 3, Height: 1}
	next := previous
	next.Y = 7
	if err = backend.sendUpdate([]Placement{previous}, []Placement{next}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(corrector.moves, []int{-1}) {
		t.Fatalf("row moves=%v", corrector.moves)
	}
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()
	select {
	case <-accepted:
		t.Fatal("row-only shift unexpectedly sent an Überzug++ add")
	case <-time.After(20 * time.Millisecond):
	}
}

func (*lifecycleBackend) Available() bool    { return true }
func (*lifecycleBackend) Update([]Placement) {}
func (b *lifecycleBackend) Close() error     { b.closed++; return nil }

func TestControllerCleanupIsIdempotentAtTerminalOwner(t *testing.T) {
	backend := &lifecycleBackend{}
	controller := NewController(ControllerOptions{Mode: "auto", Backend: backend})
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.closed != 1 {
		t.Fatalf("close calls=%d", backend.closed)
	}
}

func TestUeberzugIntegrationSmoke(t *testing.T) {
	if os.Getenv("STREAMCHAT_UEBERZUG_SMOKE") != "1" {
		t.Skip("set STREAMCHAT_UEBERZUG_SMOKE=1 for the local helper smoke test")
	}
	debug := os.Getenv("STREAMCHAT_UEBERZUG_SMOKE_DEBUG") == "1"
	backend, err := newUeberzugBackend(UeberzugBackendOptions{TerminalOutput: os.Stdout, Debug: debug, Trace: func(message string) { t.Log(message) }})
	if err != nil {
		t.Skipf("installed helper cannot attach to this test terminal: %v", err)
	}
	imagePath := os.Getenv("STREAMCHAT_UEBERZUG_SMOKE_IMAGE")
	if imagePath == "" {
		imagePath = filepath.Join(t.TempDir(), "smoke.png")
		file, createErr := os.Create(imagePath)
		if createErr != nil {
			t.Fatal(createErr)
		}
		picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				picture.Set(x, y, color.RGBA{R: 255, A: 255})
			}
		}
		if err = png.Encode(file, picture); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(imagePath); err != nil {
		t.Fatal(err)
	}
	placement := Placement{Identifier: "streamchat-smoke", Path: imagePath, X: 3, Y: 5, Width: 2, Height: 1}
	backend.Update([]Placement{placement})
	hold := 50 * time.Millisecond
	if configured := os.Getenv("STREAMCHAT_UEBERZUG_SMOKE_HOLD"); configured != "" {
		if parsed, parseErr := time.ParseDuration(configured); parseErr == nil {
			hold = parsed
		}
	}
	deadline := time.NewTimer(hold)
	ticker := time.NewTicker(min(5*time.Second, max(hold/3, 10*time.Millisecond)))
	defer deadline.Stop()
	defer ticker.Stop()
	for running := true; running; {
		select {
		case <-ticker.C:
			if placement.Y == 5 {
				placement.Y = 6
			} else {
				placement.Y = 5
			}
			backend.Update([]Placement{placement})
		case <-deadline.C:
			running = false
		}
	}
	backend.Update(nil)
	time.Sleep(100 * time.Millisecond)
	pid := backend.PID()
	if err = backend.Close(); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Fatal("Überzug++ daemon did not exit")
	}
	if _, err = os.Stat(backend.temporaryDir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory remains: %v", err)
	}
}
