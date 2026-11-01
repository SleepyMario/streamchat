package obscontrol

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIndependentBitrate(t *testing.T) {
	for _, tc := range []struct {
		name, rate                    string
		live, stale, reject, mismatch bool
		code, writes                  int
	}{
		{name: "idle 5000", rate: "5000", code: 200, writes: 1},
		{name: "live 6000", rate: "6000", live: true, code: 200, writes: 1},
		{name: "live 8000", rate: "8000", live: true, code: 200, writes: 1},
		{name: "bad value", rate: "9000", code: 400},
		{name: "missing value", rate: "", code: 400},
		{name: "helper stopped", rate: "5000", stale: true, code: 502},
		{name: "unsupported encoder", rate: "6000", reject: true, code: 502, writes: 1},
		{name: "bad readback", rate: "6000", mismatch: true, code: 502, writes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			rate, writes, starts := 8000, 0, 0
			id := "old-result"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close()
				c.WriteJSON(map[string]any{"op": 0, "d": map[string]any{"rpcVersion": 1, "authentication": map[string]string{"salt": "salt", "challenge": "challenge"}}})
				var identify map[string]any
				if c.ReadJSON(&identify) != nil {
					return
				}
				c.WriteJSON(map[string]any{"op": 2, "d": map[string]int{"negotiatedRpcVersion": 1}})
				var request struct {
					D struct {
						Type string         `json:"requestType"`
						ID   string         `json:"requestId"`
						Data map[string]any `json:"requestData"`
					} `json:"d"`
				}
				if c.ReadJSON(&request) != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				data := map[string]any{}
				ok := true
				switch request.D.Type {
				case "GetProfileParameter":
					name := request.D.Data["parameterName"].(string)
					value := map[string]any{"kbps": rate, "live": tc.live, "timestamp": time.Now().Unix()}
					if tc.stale {
						value["timestamp"] = 1
					}
					if name == "Result" {
						value = map[string]any{"id": id, "kbps": rate, "ok": !tc.reject, "error": "unsupported encoder"}
					}
					raw, _ := json.Marshal(value)
					data["parameterValue"] = string(raw)
				case "SetProfileParameter":
					writes++
					var req struct {
						ID   string `json:"id"`
						Kbps int    `json:"kbps"`
					}
					json.Unmarshal([]byte(request.D.Data["parameterValue"].(string)), &req)
					id = req.ID
					if !tc.reject && !tc.mismatch {
						rate = req.Kbps
					}
				case "StartStream":
					starts++
				default:
					t.Errorf("unexpected request: %s", request.D.Type)
				}
				c.WriteJSON(map[string]any{"op": 7, "d": map[string]any{"requestId": request.D.ID, "requestStatus": map[string]any{"result": ok, "code": 100}, "responseData": data}})
			}))
			defer server.Close()
			password := filepath.Join(t.TempDir(), "password")
			os.WriteFile(password, []byte("test"), 0600)
			h := &Handler{client: &obsClient{url: "ws" + strings.TrimPrefix(server.URL, "http"), configPath: password, timeout: time.Second}}
			response := call(h, "POST", "/obs/phone/api/stream/bitrate?kbps="+tc.rate, "", "", "")
			mu.Lock()
			defer mu.Unlock()
			if response.Code != tc.code || writes != tc.writes || starts != 0 {
				t.Fatalf("code=%d writes=%d starts=%d body=%s", response.Code, writes, starts, response.Body)
			}
		})
	}
}

func TestBitratePageControls(t *testing.T) {
	for _, label := range []string{"5,000 kbps", "6,000 kbps", "8,000 kbps"} {
		if !strings.Contains(string(page), label) {
			t.Fatal("missing choice", label)
		}
	}
	for _, old := range []string{"stream-mode", "stream-prepare", "prepareStream"} {
		if strings.Contains(string(page), old) {
			t.Fatal("obsolete coupling", old)
		}
	}
}
