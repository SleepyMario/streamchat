package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Explicitly opt in on VM700 only; never changes the program scene or outputs.
func TestIRLLiveRecovery(t *testing.T) {
	if os.Getenv("STREAMCHAT_IRL_LIVE_TEST") != "1" {
		t.Skip("requires isolated Windows OBS test")
	}
	c := &obsClient{url: "ws://127.0.0.1:4455", configPath: "C:/ProgramData/StreamchatOBS/private/obs-password", timeout: 5 * time.Second}
	ctx := context.Background()
	req := func(kind string, data any) json.RawMessage {
		t.Helper()
		v, e := c.request(ctx, kind, data)
		if e != nil {
			t.Fatal(kind, e)
		}
		return v
	}
	for _, k := range []string{"GetStreamStatus", "GetRecordStatus"} {
		var v struct {
			Active bool `json:"outputActive"`
		}
		json.Unmarshal(req(k, nil), &v)
		if v.Active {
			t.Fatal("outputs active")
		}
	}
	file := filepath.Join("C:/ProgramData/StreamchatOBS/recovery-20261101", "private-test.mp4")
	defer os.Remove(file)
	if out, e := exec.Command("C:/Program Files/StreamchatOBS/ffmpeg/ffmpeg.exe", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=25", "-t", "30", "-c:v", "libx264", "-pix_fmt", "yuv420p", file).CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	scene := "Codex private recovery test"
	source := "Codex private recovery video"
	req("CreateScene", map[string]any{"sceneName": scene})
	defer c.request(ctx, "RemoveScene", map[string]any{"sceneName": scene})
	req("CreateInput", map[string]any{"sceneName": scene, "inputName": source, "inputKind": "ffmpeg_source", "inputSettings": map[string]any{"is_local_file": true, "local_file": file, "looping": true, "close_when_inactive": false, "restart_on_activate": false}, "sceneItemEnabled": false})

	var program struct {
		Scene string `json:"currentProgramSceneName"`
	}
	json.Unmarshal(req("GetCurrentProgramScene", nil), &program)
	var item struct {
		ID int `json:"sceneItemId"`
	}
	json.Unmarshal(req("CreateSceneItem", map[string]any{"sceneName": program.Scene, "sourceName": source, "sceneItemEnabled": false}), &item)
	defer c.request(ctx, "RemoveSceneItem", map[string]any{"sceneName": program.Scene, "sceneItemId": item.ID})
	req("SetSceneItemTransform", map[string]any{"sceneName": program.Scene, "sceneItemId": item.ID, "sceneItemTransform": map[string]any{"positionX": -10000, "positionY": -10000}})
	a := &agent{obs: c}
	var s irlRecovery
	now := time.Now()
	sync := func(ready bool, gen string, sec int) {
		t.Helper()
		if e := a.item(program.Scene, source, ready); e != nil {
			t.Fatal(e)
		}
		if e := s.sync(ctx, source, ready, gen, now.Add(time.Duration(sec)*time.Second), c.request); e != nil {
			t.Fatal(e)
		}
	}
	playing := func() {
		t.Helper()
		time.Sleep(time.Second)
		var v struct {
			State string `json:"mediaState"`
		}
		json.Unmarshal(req("GetMediaInputStatus", map[string]string{"inputName": source}), &v)
		if v.State != "OBS_MEDIA_STATE_PLAYING" {
			t.Fatal("playback did not recover:", v.State)
		}
	}
	sync(true, "session1", 0)
	playing()
	sync(false, "", 2)
	sync(true, "session2", 16)
	playing()
	req("TriggerMediaInputAction", map[string]string{"inputName": source, "mediaAction": "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_STOP"})
	time.Sleep(time.Second) // Let OBS finish the asynchronous stop before testing recovery.
	sync(true, "session2", 32)
	playing()
	sync(true, "session3", 48)
	playing()
	t.Log("actual OBS playback recovered after outage, stopped decoder, and session replacement; output state unchanged")
}
