package emote

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type diagnosticLog struct {
	mu   sync.Mutex
	file *os.File
}

func newDiagnosticLog(directory string) (*diagnosticLog, error) {
	path := filepath.Join(directory, "debug.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_ = file.Chmod(0600)
	return &diagnosticLog{file: file}, nil
}

func (l *diagnosticLog) write(message string) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.file, "%s %s\n", time.Now().Format(time.RFC3339), message)
}

func (l *diagnosticLog) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	return err
}
