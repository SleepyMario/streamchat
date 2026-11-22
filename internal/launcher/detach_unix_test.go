//go:build !windows

package launcher

import "testing"

func TestDetachedCommandCreatesUnixSession(t *testing.T) {
	command := detachedCommand("/usr/bin/mpv", "https://example.com")
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("process is not detached: %+v", command.SysProcAttr)
	}
}
