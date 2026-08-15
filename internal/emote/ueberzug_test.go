package emote

import (
	"bufio"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

type lifecycleBackend struct {
	closed int
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
	backend, err := NewUeberzugBackend(os.Stdout)
	if err != nil {
		t.Skipf("installed helper cannot attach to this test terminal: %v", err)
	}
	imagePath := filepath.Join(t.TempDir(), "smoke.png")
	file, err := os.Create(imagePath)
	if err != nil {
		t.Fatal(err)
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
	placement := Placement{Identifier: "streamchat-smoke", Path: imagePath, X: 3, Y: 5, Width: 2, Height: 1}
	backend.Update([]Placement{placement})
	time.Sleep(100 * time.Millisecond)
	placement.Y = 6
	backend.Update([]Placement{placement})
	time.Sleep(100 * time.Millisecond)
	placement.Y = 5
	backend.Update([]Placement{placement})
	hold := 50 * time.Millisecond
	if configured := os.Getenv("STREAMCHAT_UEBERZUG_SMOKE_HOLD"); configured != "" {
		if parsed, parseErr := time.ParseDuration(configured); parseErr == nil {
			hold = parsed
		}
	}
	time.Sleep(hold)
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
