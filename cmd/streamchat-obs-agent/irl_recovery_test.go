package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestIRLReconnectRecovery(t *testing.T) {
	var s irlRecovery
	now := time.Unix(1000, 0)
	state := "OBS_MEDIA_STATE_PLAYING"
	restarts := 0
	fail := false
	req := func(_ context.Context, kind string, data any) (json.RawMessage, error) {
		if kind == "TriggerMediaInputAction" {
			if data.(map[string]string)["mediaAction"] == "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_PLAY" {
				return json.RawMessage(`{}`), nil
			}
			restarts++
			if fail {
				return nil, errors.New("offline")
			}
			return json.RawMessage(`{}`), nil
		}
		return json.Marshal(map[string]string{"mediaState": state})
	}
	sync := func(ready bool, gen string, seconds int) {
		t.Helper()
		if err := s.sync(context.Background(), "VPS-MMTX", ready, gen, now.Add(time.Duration(seconds)*time.Second), req); err != nil {
			t.Fatal(err)
		}
	}
	sync(false, "", 0)
	if restarts != 0 {
		t.Fatal("restarted unavailable feed")
	}
	sync(true, "session1", 2)
	sync(true, "session1", 20)
	if restarts != 1 {
		t.Fatal("restarted healthy feed", restarts)
	}
	sync(false, "", 22)
	sync(true, "session2", 24)
	if restarts != 2 {
		t.Fatal("did not restart after outage")
	}
	sync(true, "session3", 40)
	if restarts != 3 {
		t.Fatal("missed reconnect between polls")
	}
	state = "OBS_MEDIA_STATE_ERROR"
	sync(true, "session3", 56)
	sync(true, "session3", 58)
	if restarts != 4 {
		t.Fatal("failed playback retry/cooldown", restarts)
	}
	fail = true
	if err := s.sync(context.Background(), "VPS-MMTX", true, "session4", now.Add(80*time.Second), req); err == nil {
		t.Fatal("lost restart error")
	}
	fail = false
	sync(true, "session4", 82)
	if restarts != 5 {
		t.Fatal("restart storm")
	}
	sync(true, "session4", 96)
	if restarts != 6 {
		t.Fatal("did not retry failed restart")
	}
}
