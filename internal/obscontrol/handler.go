// Package obscontrol exposes narrowly scoped OBS controls inside Streamchat.
package obscontrol

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var page []byte

type Handler struct {
	client               *obsClient
	token                string
	agentURL, agentToken string
	monitor              *monitorController
	mu                   sync.Mutex
}

// FromEnv leaves OBS support disabled unless explicitly configured. Secrets stay
// in server-side files; the OBS password is never returned to the browser.
func FromEnv() (http.Handler, error) {
	endpoint := os.Getenv("STREAMCHAT_OBS_URL")
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("invalid STREAMCHAT_OBS_URL")
	}
	passwordFile := os.Getenv("STREAMCHAT_OBS_PASSWORD_FILE")
	b, err := os.ReadFile(passwordFile)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return nil, fmt.Errorf("OBS password file unavailable or empty")
	}
	agentURL := os.Getenv("STREAMCHAT_OBS_AGENT_URL")
	var agentToken string
	if agentURL != "" {
		u, e := url.Parse(agentURL)
		if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("invalid desktop agent URL")
		}
		b, e := os.ReadFile(os.Getenv("STREAMCHAT_OBS_AGENT_TOKEN_FILE"))
		if e != nil || len(strings.TrimSpace(string(b))) < 32 {
			return nil, fmt.Errorf("desktop agent token unavailable")
		}
		agentToken = strings.TrimSpace(string(b))
	}
	monitor := newMonitorController(monitorConfig{RelayStatusURL: os.Getenv("STREAMCHAT_OBS_RELAY_STATUS_URL"), BananaStatusURL: os.Getenv("STREAMCHAT_OBS_BANANA_URL")})
	if file := os.Getenv("STREAMCHAT_OBS_BANANA_TOKEN_FILE"); file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, e
		}
		monitor.cfg.BananaToken = strings.TrimSpace(string(b))
	}
	if e := validateMonitorConfig(monitor.cfg); e != nil {
		return nil, e
	}
	return &Handler{monitor: monitor, agentURL: strings.TrimRight(agentURL, "/"), agentToken: agentToken, client: &obsClient{url: endpoint, configPath: passwordFile, timeout: 5 * time.Second}, token: ""}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	remote, _, remoteErr := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(remote)
	_, vpn, _ := net.ParseCIDR("10.77.0.0/24")
	host, _, hostErr := net.SplitHostPort(r.Host)
	if remoteErr != nil || ip == nil || !vpn.Contains(ip) || hostErr != nil || host != "10.77.0.1" {
		http.Error(w, "Controller is available only through its private WireGuard address", 403)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/obs/" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		_, _ = w.Write(page)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/obs/phone/") && r.URL.Path != "/obs/api/status" && r.URL.Path != "/obs/api/control" && r.URL.Path != "/obs/api/desktop" {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "foreign origin", 403)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/obs/phone/") {
		h.phone(w, r)
		return
	}
	if r.URL.Path == "/obs/api/desktop" {
		h.desktop(w, r)
		return
	}
	if r.URL.Path == "/obs/api/status" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		scenes, current, err := h.client.scenes(r.Context())
		if err != nil {
			http.Error(w, "OBS unavailable or authentication failed", 502)
			return
		}
		stream, err := h.client.request(r.Context(), "GetStreamStatus", nil)
		if err != nil {
			http.Error(w, "OBS stream status unavailable", 502)
			return
		}
		record, err := h.client.request(r.Context(), "GetRecordStatus", nil)
		if err != nil {
			http.Error(w, "OBS recording status unavailable", 502)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"scenes": scenes, "current_scene": current, "stream": stream, "record": record})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "JSON required", 415)
		return
	}
	var in struct {
		Action string `json:"action"`
		Scene  string `json:"scene,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if d.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var err error
	if in.Action == "scene" {
		if strings.TrimSpace(in.Scene) == "" || len(in.Scene) > 512 {
			http.Error(w, "invalid scene", 400)
			return
		}
		err = h.client.switchScene(r.Context(), in.Scene)
	} else {
		methods := map[string]string{"start_stream": "StartStream", "stop_stream": "StopStream", "start_record": "StartRecord", "stop_record": "StopRecord"}
		method, ok := methods[in.Action]
		if !ok {
			http.Error(w, "unsupported action", 400)
			return
		}
		_, err = h.client.request(r.Context(), method, nil)
	}
	if err != nil {
		http.Error(w, "OBS command failed; refresh status before retrying", 502)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h *Handler) desktop(w http.ResponseWriter, r *http.Request) {
	if h.agentURL == "" {
		http.Error(w, "Desktop agent is not configured", 503)
		return
	}
	path := "/status"
	var payload []byte
	if r.Method == http.MethodPost {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "JSON required", 415)
			return
		}
		var in struct {
			Action string `json:"action"`
			Layout string `json:"layout,omitempty"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		switch in.Action {
		case "subtitles_start":
			path = "/subtitles/start"
		case "subtitles_stop":
			path = "/subtitles/stop"
		case "gps":
			if in.Layout != "Dashboard" && in.Layout != "Minimal" && in.Layout != "Full" && in.Layout != "Off" {
				http.Error(w, "invalid layout", 400)
				return
			}
			path = "/gps"
			payload, _ = json.Marshal(map[string]string{"layout": in.Layout})
		default:
			http.Error(w, "unsupported action", 400)
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, r.Method, h.agentURL+path, bytes.NewReader(payload))
	if e != nil {
		http.Error(w, "agent request failed", 502)
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.agentToken)
	req.Header.Set("Content-Type", "application/json")
	res, e := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		http.Error(w, "Desktop agent unavailable", 502)
		return
	}
	defer res.Body.Close()
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 65536))
}
