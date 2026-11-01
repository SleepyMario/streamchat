package obscontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
func (h *Handler) agentRequest(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	if h.agentURL == "" {
		return nil, fmt.Errorf("Windows agent not configured")
	}
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req, e := http.NewRequestWithContext(ctx, method, h.agentURL+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+h.agentToken)
	req.Header.Set("Content-Type", "application/json")
	res, e := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		return nil, fmt.Errorf("Windows agent unreachable")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if e != nil {
		return nil, e
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(b)))
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("Invalid agent response")
	}
	return b, nil
}
func (h *Handler) phone(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/obs/phone")
	if r.Method != "GET" && r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "Method not allowed"})
		return
	}
	fail := func(e error) { writeJSON(w, 502, map[string]string{"error": e.Error()}) }
	agent := func(method, path string, payload any) {
		b, e := h.agentRequest(r.Context(), method, path, payload)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, b)
	}
	if r.Method == "GET" {
		switch path {
		case "/api/stream/bitrate":
			s, e := h.client.bitrateStatus(r.Context())
			if e != nil {
				fail(e)
				return
			}
			writeJSON(w, 200, s)
		case "/api/obs", "/obs":
			agent("GET", "/obs", nil)
		case "/api/scenes":
			s, c, e := h.client.scenes(r.Context())
			if e != nil {
				fail(fmt.Errorf("OBS unavailable"))
				return
			}
			writeJSON(w, 200, map[string]any{"scenes": s, "current": c})
		case "/api/outputs":
			s, e := h.client.request(r.Context(), "GetStreamStatus", nil)
			if e != nil {
				fail(fmt.Errorf("Stream status unavailable"))
				return
			}
			v, e := h.client.request(r.Context(), "GetRecordStatus", nil)
			if e != nil {
				fail(fmt.Errorf("Recording status unavailable"))
				return
			}
			var a, b map[string]any
			json.Unmarshal(s, &a)
			json.Unmarshal(v, &b)
			writeJSON(w, 200, map[string]any{"available": true, "streaming": a["outputActive"], "recording": b["outputActive"], "recording_paused": b["outputPaused"]})
		case "/api/subtitles":
			b, e := h.agentRequest(r.Context(), "GET", "/status", nil)
			if e != nil {
				fail(e)
				return
			}
			var d map[string]any
			json.Unmarshal(b, &d)
			writeJSON(w, 200, map[string]any{"state": d["subtitles"], "message": d["message"], "mode": "remote", "local_available": false})
		case "/api/chat":
			agent("GET", "/chat", nil)
		case "/gps":
			agent("GET", "/status", nil)
		case "/api/monitor/stream":
			h.monitor.streamStatus(w, r)
		case "/api/monitor/banana":
			h.monitor.bananaStatus(w, r)
		default:
			writeJSON(w, 404, map[string]string{"error": "Unknown endpoint"})
		}
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch path {
	case "/api/stream/bitrate":
		kbps, e := strconv.Atoi(r.URL.Query().Get("kbps"))
		if e != nil || (kbps != 5000 && kbps != 6000 && kbps != 8000) {
			writeJSON(w, 400, map[string]string{"error": "Choose 5000, 6000 or 8000 kbps"})
			return
		}
		state, e := h.client.setBitrate(r.Context(), kbps)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, state)
	case "/api/obs/start":
		agent("POST", "/obs/start", nil)
	case "/api/obs/stop":
		agent("POST", "/obs/stop", nil)
	case "/api/subtitles/start":
		agent("POST", "/subtitles/start", nil)
	case "/api/subtitles/stop":
		agent("POST", "/subtitles/stop", nil)
	default:
		if strings.HasPrefix(path, "/gps/") {
			layout := strings.TrimPrefix(path, "/gps/")
			if layout != "Dashboard" && layout != "Minimal" && layout != "Full" && layout != "Off" {
				writeJSON(w, 400, map[string]string{"error": "Invalid GPS layout"})
				return
			}
			agent("POST", "/gps", map[string]string{"layout": layout})
			return
		}
		if strings.HasPrefix(path, "/api/monitor/banana/recover/") {
			r.SetPathValue("action", strings.TrimPrefix(path, "/api/monitor/banana/recover/"))
			h.monitor.bananaRecovery(w, r)
			return
		}
		if strings.HasPrefix(path, "/api/scenes/") {
			e := h.client.switchScene(r.Context(), strings.TrimPrefix(path, "/api/scenes/"))
			if e != nil {
				fail(fmt.Errorf("Scene switch failed; refresh before retry"))
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		methods := map[string]string{"/api/outputs/stream/start": "StartStream", "/api/outputs/stream/stop": "StopStream", "/api/outputs/record/start": "StartRecord", "/api/outputs/record/stop": "StopRecord"}
		if method, ok := methods[path]; ok {
			_, e := h.client.request(r.Context(), method, nil)
			if e != nil {
				fail(fmt.Errorf("Output command failed; refresh before retry"))
				return
			}
			writeJSON(w, 200, map[string]string{"message": "OBS command accepted"})
			return
		}
		writeJSON(w, 400, map[string]string{"error": "Unsupported action"})
	}
}
