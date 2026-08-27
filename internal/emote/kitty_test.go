package emote

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectKitty(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "term", env: map[string]string{"TERM": "xterm-kitty"}, want: true},
		{name: "window id", env: map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "7"}, want: true},
		{name: "other terminal", env: map[string]string{"TERM": "alacritty"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := detectKitty(func(key string) string { return test.env[key] })
			if got != test.want {
				t.Fatalf("detectKitty=%v want=%v", got, test.want)
			}
		})
	}
}

func TestDefaultControllerDoesNotCreateGraphicalStateOutsideKitty(t *testing.T) {
	t.Setenv("TERM", "alacritty")
	t.Setenv("KITTY_WINDOW_ID", "")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	controller := NewDefaultControllerWithOptions(DefaultControllerOptions{Mode: "auto", TerminalOutput: io.Discard, Debug: true})
	if controller.Available() {
		t.Fatal("non-Kitty terminal unexpectedly enabled graphical backend")
	}
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("non-Kitty fallback created graphical cache state: %v", err)
	}
}

func TestKittyBackendTransmitsConfirmsScrollsAndDeletes(t *testing.T) {
	path := writeKittyTestPNG(t)
	var output bytes.Buffer
	backend, err := newKittyBackend(KittyBackendOptions{
		TerminalOutput: &output,
		Getenv: func(key string) string {
			if key == "TERM" {
				return "xterm-kitty"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidated := make(chan struct{}, 2)
	backend.SetInvalidation(func() { invalidated <- struct{}{} })
	first := Placement{Identifier: "first", Path: path, X: 4, Y: 8, Width: 3, Height: 1}
	backend.Update([]Placement{first})
	if !backend.Confirmed("first") {
		t.Fatal("successful placement was not confirmed")
	}
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("first confirmation did not request fallback redraw")
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b7\x1b[9;5H") || !strings.Contains(raw, "a=T,f=100") || !strings.Contains(raw, "c=3,r=1,C=1") || !strings.Contains(raw, "q=2") || !strings.HasSuffix(raw, "\x1b8") {
		t.Fatalf("unexpected Kitty transmission: %q", raw)
	}
	output.Reset()
	backend.Reset()
	if !backend.Confirmed("first") || !strings.Contains(output.String(), "a=d,d=I") {
		t.Fatalf("redraw reset lost confirmation or placement: confirmed=%v output=%q", backend.Confirmed("first"), output.String())
	}
	backend.Update([]Placement{first})
	select {
	case <-invalidated:
		t.Fatal("redraw reset caused a second confirmation cycle")
	case <-time.After(25 * time.Millisecond):
	}

	output.Reset()
	second := Placement{Identifier: "second", Path: path, X: 10, Y: 8, Width: 3, Height: 1}
	first.Y--
	backend.Update([]Placement{first, second})
	raw = output.String()
	if strings.Contains(raw, "i="+uintString(kittyImageID("first"))) || !strings.Contains(raw, "i="+uintString(kittyImageID("second"))) {
		t.Fatalf("native scroll retransmitted existing image or missed new image: %q", raw)
	}

	output.Reset()
	backend.Update(nil)
	if backend.Confirmed("first") || backend.Confirmed("second") {
		t.Fatal("removed placements remained confirmed")
	}
	if count := strings.Count(output.String(), "a=d,d=I"); count != 2 {
		t.Fatalf("deletion count=%d output=%q", count, output.String())
	}
	if err = backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestKittyBackendRejectsNonPNGAndKeepsFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-image.img")
	if err := os.WriteFile(path, []byte("not a png"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	backend, err := newKittyBackend(KittyBackendOptions{TerminalOutput: &output, Getenv: func(string) string { return "xterm-kitty" }})
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic error
	backend.SetDiagnostic(func(err error) { diagnostic = err })
	backend.Update([]Placement{{Identifier: "bad", Path: path, Width: 3, Height: 1}})
	if backend.Confirmed("bad") || diagnostic == nil || !strings.Contains(diagnostic.Error(), "not PNG") {
		t.Fatalf("confirmed=%v diagnostic=%v", backend.Confirmed("bad"), diagnostic)
	}
	if strings.Contains(output.String(), "a=T") {
		t.Fatalf("invalid data was transmitted: %q", output.String())
	}
}

func writeKittyTestPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "emote.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 3, 2))
	picture.Set(1, 1, color.RGBA{R: 255, A: 255})
	if err = png.Encode(file, picture); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func uintString(value uint32) string {
	return fmt.Sprintf("%d", value)
}
