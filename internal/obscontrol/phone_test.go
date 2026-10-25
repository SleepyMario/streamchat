package obscontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPhoneAuthorizationAndRouting(t *testing.T) {
	h, _, _ := fixture(t)
	h.monitor = newMonitorController(monitorConfig{})
	r := httptest.NewRequest("GET", "http://10.77.0.1:8788/obs/phone/api/obs", nil)
	r.RemoteAddr = "192.0.2.1:5555"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("Public client accepted")
	}
	r = httptest.NewRequest("GET", "http://evil.test/obs/", nil)
	r.RemoteAddr = "10.77.0.5:5555"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("Foreign host accepted")
	}
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-secret" {
			t.Error("missing agent authentication")
		}
		json.NewEncoder(w).Encode(map[string]any{"state": "running", "recent_chat": []any{}, "subtitles": "off"})
	}))
	defer a.Close()
	h.agentURL = a.URL
	h.agentToken = "agent-secret"
	for _, p := range []string{"/api/obs", "/api/chat", "/api/subtitles", "/api/scenes", "/api/outputs", "/api/monitor/stream", "/api/monitor/banana"} {
		w := call(h, "GET", "/obs/phone"+p, "", "Bearer "+h.token, "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s %d %s", p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/api/obs/start", "/api/obs/stop", "/api/outputs/record/start", "/api/scenes/Break", "/gps/Off"} {
		w := call(h, "POST", "/obs/phone"+p, "", "Bearer "+h.token, "https://evil.test")
		if w.Code != 403 {
			t.Fatal("foreign origin accepted")
		}
	}
	w = call(h, "POST", "/obs/phone/api/obs/reboot", "", "Bearer "+h.token, "")
	if w.Code != 400 {
		t.Fatal("unbounded action accepted")
	}
	w = call(h, "GET", "/obs/", "", "", "")
	if strings.Contains(w.Body.String(), "<iframe") || !strings.Contains(w.Body.String(), "Live feed — not ready yet") {
		t.Fatal("feed placeholder missing")
	}
}
