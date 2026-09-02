package bot

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// CommandRecord is intentionally minimal. It records that a recognized bot
// command actually ran, without retaining chat, identity, channel, arguments,
// replies, provider errors, or unrelated automation events.
type CommandRecord struct {
	Time      time.Time `json:"time"`
	Platform  string    `json:"platform"`
	Command   string    `json:"command"`
	Succeeded bool      `json:"succeeded"`
}

type CommandRecorder interface {
	WriteCommand(CommandRecord) error
}

type CommandLog struct {
	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	failed error
}

func OpenCommandLog(path string) (*CommandLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &CommandLog{file: file, writer: bufio.NewWriter(file)}, nil
}

func (l *CommandLog) WriteCommand(record CommandRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	payload, err := json.Marshal(record)
	if err == nil {
		_, err = l.writer.Write(append(payload, '\n'))
	}
	if err == nil {
		err = l.writer.Flush()
	}
	if err != nil {
		l.failed = err
	}
	return err
}

func (l *CommandLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("bot command log already closed")
	}
	flushErr := l.writer.Flush()
	closeErr := l.file.Close()
	l.file = nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}
