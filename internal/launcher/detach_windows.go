//go:build windows

package launcher

import "os/exec"

func detach(command *exec.Cmd) {}
