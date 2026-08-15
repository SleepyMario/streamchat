package emote

import (
	"bufio"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestStopProcessLeavesNoHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process signal semantics differ")
	}
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	stopProcess(command, done)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("helper process remained alive")
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
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err = png.Encode(file, picture); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	backend.Update([]Placement{{Identifier: "streamchat-smoke", Path: imagePath, X: 0, Y: 0, Width: 1, Height: 1}})
	time.Sleep(50 * time.Millisecond)
	if err = backend.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.command.ProcessState == nil || !backend.command.ProcessState.Exited() {
		t.Fatal("Überzug++ helper did not exit")
	}
	if _, err = os.Stat(backend.temporaryDir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory remains: %v", err)
	}
}
