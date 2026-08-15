package launcher

import (
	"errors"
	"io"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestMpvPreferredAndBrowserFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		available map[string]string
		wantPath  string
	}{
		{name: "mpv preferred", available: map[string]string{"mpv": "/usr/bin/mpv", "xdg-open": "/usr/bin/xdg-open"}, wantPath: "/usr/bin/mpv"},
		{name: "browser fallback", available: map[string]string{"xdg-open": "/usr/bin/xdg-open"}, wantPath: "/usr/bin/xdg-open"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var path string
			var args []string
			l := &Launcher{
				lookPath: func(name string) (string, error) {
					if found := test.available[name]; found != "" {
						return found, nil
					}
					return "", errors.New("not found")
				},
				start: func(gotPath string, gotArgs ...string) error {
					path, args = gotPath, gotArgs
					return nil
				},
			}
			if err := l.Open("https://example.com/channel"); err != nil {
				t.Fatal(err)
			}
			if path != test.wantPath || !reflect.DeepEqual(args, []string{"https://example.com/channel"}) {
				t.Fatalf("path=%q args=%v", path, args)
			}
		})
	}
}

func TestUnavailableAndStartFailure(t *testing.T) {
	unavailable := &Launcher{lookPath: func(string) (string, error) { return "", errors.New("not found") }}
	if err := unavailable.Open("https://example.com"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v", err)
	}
	failing := &Launcher{
		lookPath: func(string) (string, error) { return "/usr/bin/mpv", nil },
		start:    func(string, ...string) error { return errors.New("private system details") },
	}
	if got := failing.Open("https://example.com"); got == nil || got.Error() != "could not start mpv" {
		t.Fatalf("error=%v", got)
	}
}

func TestDetachedCommandDoesNotUseTerminalStreams(t *testing.T) {
	command := detachedCommand("/usr/bin/mpv", "https://example.com")
	if command.Stdin != nil || command.Stdout != io.Discard || command.Stderr != io.Discard {
		t.Fatalf("stdin=%v stdout=%v stderr=%v", command.Stdin, command.Stdout, command.Stderr)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("process is not detached: %+v", command.SysProcAttr)
	}
}

func TestDetachedStartDoesNotWaitForChildExit(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep executable unavailable")
	}
	started := time.Now()
	if err = startDetached(sleep, "0.25"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("detached start blocked for %s", elapsed)
	}
	// Let the package's asynchronous waiter reap the short-lived child.
	time.Sleep(300 * time.Millisecond)
}
