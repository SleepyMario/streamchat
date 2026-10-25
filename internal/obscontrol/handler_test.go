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

// A local fake OBS validates the challenge handshake and handles only fixtures.
func fixture(t *testing.T) (*Handler, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"op": 0, "d": map[string]any{"rpcVersion": 1, "authentication": map[string]string{"salt": "fixture-salt", "challenge": "fixture-challenge"}}})
		var ident struct {
			Op int `json:"op"`
			D  struct {
				Auth string `json:"authentication"`
			} `json:"d"`
		}
		if c.ReadJSON(&ident) != nil {
			return
		}
		if ident.Op != 1 || ident.D.Auth != obsAuthentication("invented-password", "fixture-salt", "fixture-challenge") {
			t.Error("bad authentication")
			return
		}
		_ = c.WriteJSON(map[string]any{"op": 2, "d": map[string]int{"negotiatedRpcVersion": 1}})
		var req struct {
			Op int `json:"op"`
			D  struct {
				Type string         `json:"requestType"`
				ID   string         `json:"requestId"`
				Data map[string]any `json:"requestData"`
			} `json:"d"`
		}
		if c.ReadJSON(&req) != nil {
			return
		}
		mu.Lock()
		calls = append(calls, req.D.Type)
		mu.Unlock()
		var data any = map[string]any{"outputActive": false}
		ok := true
		if req.D.Type == "GetSceneList" {
			data = map[string]any{"currentProgramSceneName": "Camera", "scenes": []any{map[string]string{"sceneName": "Camera"}, map[string]string{"sceneName": "Break"}}}
		}
		if req.D.Type == "SetCurrentProgramScene" {
			ok = req.D.Data["sceneName"] == "Break"
		}
		_ = c.WriteJSON(map[string]any{"op": 7, "d": map[string]any{"requestId": req.D.ID, "requestType": req.D.Type, "requestStatus": map[string]any{"result": ok, "code": 100}, "responseData": data}})
	}))
	t.Cleanup(srv.Close)
	f := filepath.Join(t.TempDir(), "password")
	if e := os.WriteFile(f, []byte("invented-password"), 0600); e != nil {
		t.Fatal(e)
	}
	return &Handler{client: &obsClient{url: "ws" + strings.TrimPrefix(srv.URL, "http"), configPath: f, timeout: time.Second}, token: strings.Repeat("x", 32)}, &calls, &mu
}
func call(h http.Handler, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://10.77.0.1:8788"+path, strings.NewReader(body))
	r.RemoteAddr = "10.77.0.5:12345"
	r.Header.Set("Authorization", token)
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestControls(t *testing.T) {
	h, calls, mu := fixture(t)
	token := "Bearer " + h.token
	w := call(h, "GET", "/obs/api/status", "", token, "")
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	var s struct {
		Current string   `json:"current_scene"`
		Scenes  []string `json:"scenes"`
	}
	if json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Current != "Camera" || len(s.Scenes) != 2 {
		t.Fatal(w.Body)
	}
	for _, body := range []string{`{"action":"scene","scene":"Break"}`, `{"action":"start_stream"}`, `{"action":"stop_stream"}`, `{"action":"start_record"}`, `{"action":"stop_record"}`} {
		if w := call(h, "POST", "/obs/api/control", body, token, "http://10.77.0.1:8788"); w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := call(h, "POST", "/obs/api/control", `{"action":"scene","scene":"Missing"}`, token, ""); w.Code != 502 {
		t.Fatal(w.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 9 {
		t.Fatalf("unexpected calls %v", *calls)
	}
}
func TestRejectedRequestsNeverReachOBS(t *testing.T) {
	h, calls, mu := fixture(t)
	for _, tc := range []struct {
		method, path, body, token, origin string
		code                              int
	}{
		{"POST", "/obs/api/control", `{"action":"start_stream"}`, "Bearer " + h.token, "https://attacker.test", 403},
		{"GET", "/obs/api/control", "", "Bearer " + h.token, "", 405},
		{"POST", "/obs/api/control", `{"action":"shell"}`, "Bearer " + h.token, "", 400},
		{"POST", "/obs/api/control", `{"action":"scene"}`, "Bearer " + h.token, "", 400},
		{"POST", "/obs/api/control", `{"action":"start_stream","unknown":1}`, "Bearer " + h.token, "", 400},
		{"POST", "/obs/api/control", `{"action":"start_stream"}{}`, "Bearer " + h.token, "", 400},
		{"POST", "/obs/api/control", strings.Repeat("x", 5000), "Bearer " + h.token, "", 400},
	} {
		if w := call(h, tc.method, tc.path, tc.body, tc.token, tc.origin); w.Code != tc.code {
			t.Errorf("%s got %d want %d", tc.body, w.Code, tc.code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 0 {
		t.Fatalf("rejected requests reached OBS: %v", *calls)
	}
}
func TestOptionalConfiguration(t *testing.T) {
	t.Setenv("STREAMCHAT_OBS_URL", "")
	if h, e := FromEnv(); e != nil || h != nil {
		t.Fatal("must be disabled by default")
	}
	t.Setenv("STREAMCHAT_OBS_URL", "http://invalid")
	if _, e := FromEnv(); e == nil {
		t.Fatal("invalid scheme accepted")
	}
	t.Setenv("STREAMCHAT_OBS_URL", "ws://example.test:4455")
	t.Setenv("STREAMCHAT_OBS_PASSWORD_FILE", "/nonexistent")
	if _, e := FromEnv(); e == nil {
		t.Fatal("missing secret accepted")
	}
}
func TestPageDoesNotContainSecrets(t *testing.T) {
	h, _, _ := fixture(t)
	w := call(h, "GET", "/obs/", "", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), h.token) || strings.Contains(w.Body.String(), "invented-password") {
		t.Fatal("page leaked secrets or failed")
	}
}

func TestDesktopProxy(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer private-agent-token" {
			t.Error("missing agent authorization")
		}
		if r.URL.Path != "/gps" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()
	h := &Handler{token: strings.Repeat("t", 32), agentURL: backend.URL, agentToken: "private-agent-token"}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"action":"gps","layout":"Full"}`, 200}, {`{"action":"exec"}`, 400}, {`{"action":"gps","layout":"bad"}`, 400}, {`{"action":"gps","layout":"Off"} {}`, 400}} {
		r := httptest.NewRequest("POST", "http://10.77.0.1:8788/obs/api/desktop", strings.NewReader(tc.body))
		r.RemoteAddr = "10.77.0.5:12345"
		r.Header.Set("Authorization", "Bearer "+h.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s: %d", tc.body, w.Code)
		}
	}
	if calls != 1 {
		t.Fatal("invalid commands reached desktop", calls)
	}
}
