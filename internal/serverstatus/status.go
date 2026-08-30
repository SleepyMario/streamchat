package serverstatus

import (
	"context"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientruntime"
	"github.com/SleepyMario/streamchat/internal/streamprobe"
)

const SchemaVersion = 1

type Snapshot struct {
	SchemaVersion int                                   `json:"schema_version"`
	GeneratedAt   time.Time                             `json:"generated_at"`
	Media         streamprobe.State                     `json:"media"`
	Channel       *clientruntime.StreamStatus           `json:"channel,omitempty"`
	Channels      map[string]clientruntime.StreamStatus `json:"channels"`
	RecentChat    []chat.Message                        `json:"recent_chat,omitempty"`
}

type Service struct {
	probe   *streamprobe.Probe
	runtime *clientruntime.Runtime
	mu      sync.RWMutex
	chat    []chat.Message
}

// Observe retains a small process-local window for optional status consumers.
// Durable history remains the archive's responsibility.
func (s *Service) Observe(message chat.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chat = append([]chat.Message{message}, s.chat...)
	if len(s.chat) > 50 {
		s.chat = s.chat[:50]
	}
}

func New(probe *streamprobe.Probe, runtime *clientruntime.Runtime) *Service {
	return &Service{probe: probe, runtime: runtime}
}

func (s *Service) Run(ctx context.Context) {
	go s.probe.Run(ctx)
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		s.runtime.RefreshStatus(refreshCtx)
	}
	refresh()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (s *Service) Snapshot() Snapshot {
	state := s.runtime.State()
	channels := state.Streams
	var canonical *clientruntime.StreamStatus
	if status, ok := channels[state.Selected]; ok && status.Available {
		value := status
		canonical = &value
	} else {
		for _, platform := range []string{"kick", "twitch"} {
			if status, ok := channels[platform]; ok && status.Available {
				value := status
				canonical = &value
				break
			}
		}
	}
	s.mu.RLock()
	recent := append([]chat.Message(nil), s.chat...)
	s.mu.RUnlock()
	return Snapshot{SchemaVersion: SchemaVersion, GeneratedAt: time.Now(), Media: s.probe.State(), Channel: canonical, Channels: channels, RecentChat: recent}
}
