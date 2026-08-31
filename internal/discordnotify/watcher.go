package discordnotify

import (
	"context"
	"errors"
	"time"

	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

type Sender interface {
	Send(context.Context, string) error
}

type Watcher struct {
	Sender        Sender
	State         func() streamprobe.State
	BuildMessage  func(context.Context) (string, error)
	PollInterval  time.Duration
	Confirmations int
	OnError       func(error)
}

func (w Watcher) Run(ctx context.Context) {
	interval := w.PollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	confirmations := w.Confirmations
	if confirmations < 1 {
		confirmations = 2
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var initialized, stableOnline, candidateOnline, armed bool
	var candidateCount int
	var lastCheck time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := w.State()
			if state.CheckedAt.IsZero() || state.CheckedAt.Equal(lastCheck) {
				continue
			}
			lastCheck = state.CheckedAt
			if candidateCount == 0 || candidateOnline != state.Online {
				candidateOnline = state.Online
				candidateCount = 1
			} else {
				candidateCount++
			}
			if candidateCount < confirmations {
				continue
			}
			candidateCount = 0
			if !initialized {
				initialized = true
				stableOnline = candidateOnline
				armed = !stableOnline
				continue
			}
			if candidateOnline == stableOnline {
				continue
			}
			stableOnline = candidateOnline
			if !stableOnline {
				armed = true
				continue
			}
			if !armed {
				continue
			}
			if w.BuildMessage == nil {
				stableOnline = false
				if w.OnError != nil {
					w.OnError(errors.New("Discord announcement builder is unavailable"))
				}
				continue
			}
			message, err := w.BuildMessage(ctx)
			if err == nil {
				err = w.Sender.Send(ctx, message)
			}
			if err != nil {
				stableOnline = false
				if w.OnError != nil {
					w.OnError(err)
				}
				continue
			}
			armed = false
		}
	}
}
