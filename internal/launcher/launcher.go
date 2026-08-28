package launcher

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
)

var ErrUnavailable = errors.New("neither mpv nor xdg-open is available")

// Launcher opens public stream URLs without attaching the child to Streamchat's
// terminal session. The function fields keep process execution injectable.
type Launcher struct {
	lookPath func(string) (string, error)
	start    func(string, ...string) error
}

func New() *Launcher {
	return &Launcher{lookPath: exec.LookPath, start: startDetached}
}

func (l *Launcher) Open(url string) error {
	if l == nil || l.lookPath == nil || l.start == nil {
		return ErrUnavailable
	}
	for _, name := range []string{"mpv", "xdg-open"} {
		path, err := l.lookPath(name)
		if err != nil {
			continue
		}
		if err = l.start(path, url); err != nil {
			return errors.New("could not start " + filepath.Base(path))
		}
		return nil
	}
	return ErrUnavailable
}

func startDetached(path string, args ...string) error {
	command := detachedCommand(path, args...)
	if err := command.Start(); err != nil {
		return err
	}
	go func() {
		_ = command.Wait()
	}()
	return nil
}

func detachedCommand(path string, args ...string) *exec.Cmd {
	command := exec.Command(path, args...)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	detach(command)
	return command
}
