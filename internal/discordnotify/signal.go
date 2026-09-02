package discordnotify

import (
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

// Signal holds the authoritative lifecycle state reported by MediaMTX for the
// continuous OBS input. State returns a fresh observation so Watcher can apply
// its normal transition confirmation to a single ready/not-ready edge.
type Signal struct {
	mu     sync.RWMutex
	online bool
}

func (s *Signal) Set(online bool) {
	s.mu.Lock()
	s.online = online
	s.mu.Unlock()
}

func (s *Signal) State() streamprobe.State {
	s.mu.RLock()
	online := s.online
	s.mu.RUnlock()
	return streamprobe.State{Online: online, CheckedAt: time.Now()}
}
