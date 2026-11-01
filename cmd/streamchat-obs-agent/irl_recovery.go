package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

type irlRecovery struct {
	ready       bool
	generation  string
	lastRestart time.Time
}

// A TCP connection alone is not a recovered media source. Reinitialize OBS's
// decoder for each ready session and retry failed playback without a restart loop.
func (s *irlRecovery) sync(ctx context.Context, source string, ready bool, generation string, now time.Time, request func(context.Context, string, any) (json.RawMessage, error)) error {
	if !ready {
		s.ready = false
		s.generation = ""
		return nil
	}
	reconnect := !s.ready || s.generation != generation
	restart := reconnect
	if !restart && now.Sub(s.lastRestart) >= 15*time.Second {
		raw, err := request(ctx, "GetMediaInputStatus", map[string]string{"inputName": source})
		if err != nil {
			return err
		}
		var status struct {
			State string `json:"mediaState"`
		}
		if err = json.Unmarshal(raw, &status); err != nil {
			return err
		}
		switch status.State {
		case "OBS_MEDIA_STATE_ERROR", "OBS_MEDIA_STATE_ENDED", "OBS_MEDIA_STATE_STOPPED", "OBS_MEDIA_STATE_NONE":
			restart = true
		}
	}
	if restart {
		// Bound retries even if OBS rejects the action or remains in an error state.
		if !s.lastRestart.IsZero() && now.Sub(s.lastRestart) < 15*time.Second {
			return nil
		}
		s.lastRestart = now
		if _, err := request(ctx, "TriggerMediaInputAction", map[string]string{"inputName": source, "mediaAction": "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_RESTART"}); err != nil {
			return fmt.Errorf("restart media: %w", err)
		}
		log.Printf("IRL recovery %s: restarted media input (new session=%t)", source, reconnect)
	}
	s.ready = true
	s.generation = generation
	return nil
}
