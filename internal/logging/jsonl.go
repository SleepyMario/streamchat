package logging

import (
	"bufio"
	"encoding/json"
	"errors"
	"github.com/SleepyMario/streamchat/internal/chat"
	"os"
	"sync"
)

type Logger struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	failed error
}

func Open(p string) (*Logger, error) {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return nil, e
	}
	_ = f.Chmod(0600)
	return &Logger{f: f, w: bufio.NewWriter(f)}, nil
}
func (l *Logger) Write(m chat.Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	b, e := json.Marshal(m)
	if e == nil {
		_, e = l.w.Write(append(b, '\n'))
	}
	if e == nil {
		e = l.w.Flush()
	}
	if e != nil {
		l.failed = e
	}
	return e
}
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("logger already closed")
	}
	e1 := l.w.Flush()
	e2 := l.f.Close()
	l.f = nil
	if e1 != nil {
		return e1
	}
	return e2
}
