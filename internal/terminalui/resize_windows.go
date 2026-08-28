//go:build windows

package terminalui

import "os"

func notifyResize(ch chan<- os.Signal) {}
