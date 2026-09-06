// Package inputtrigger turns a confirmed MediaMTX input edge into one
// idempotent action. It deliberately ignores the initial online state so a
// Streamchat restart cannot repeat an action for a stream already in progress.
package inputtrigger

import (
	"context"
	"errors"
	"time"

	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

type Watcher struct {
	State         func() streamprobe.State
	OnOnline      func(context.Context) error
	PollInterval  time.Duration
	RetryDelay    time.Duration
	Confirmations int
	OnError       func(error)
}

func (w Watcher) Run(ctx context.Context) {
	interval := w.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	confirmations := w.Confirmations
	if confirmations < 1 {
		confirmations = 1
	}
	retryDelay := w.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var initialized, stableOnline, candidateOnline, armed bool
	var candidateCount int
	var lastCheck time.Time
	var nextAttempt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.State == nil {
				if w.OnError != nil {
					w.OnError(errors.New("input lifecycle state is unavailable"))
				}
				return
			}
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
			if candidateOnline != stableOnline {
				stableOnline = candidateOnline
				if !stableOnline {
					armed = true
					nextAttempt = time.Time{}
				}
			}
			if !stableOnline || !armed || (!nextAttempt.IsZero() && time.Now().Before(nextAttempt)) {
				continue
			}
			if w.OnOnline == nil {
				if w.OnError != nil {
					w.OnError(errors.New("input-online action is unavailable"))
				}
				return
			}
			if err := w.OnOnline(ctx); err != nil {
				nextAttempt = time.Now().Add(retryDelay)
				if w.OnError != nil {
					w.OnError(err)
				}
				continue
			}
			armed = false
			nextAttempt = time.Time{}
		}
	}
}
