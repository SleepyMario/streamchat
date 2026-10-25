package obscontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type monitorConfig struct {
	RelayStatusURL  string
	BananaStatusURL string
	BananaToken     string
	ViewerURL       string
}

type monitorController struct {
	cfg  monitorConfig
	http *http.Client
}

type relayDestination struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	Restarts  uint64 `json:"restarts"`
	LastError string `json:"last_error,omitempty"`
}

type relaySnapshot struct {
	InputReady   bool               `json:"input_ready"`
	Destinations []relayDestination `json:"destinations"`
}

type platformOutput struct {
	Enabled   bool   `json:"enabled"`
	Online    bool   `json:"online"`
	State     string `json:"state"`
	Restarts  uint64 `json:"restarts,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type streamMonitorStatus struct {
	TokyoReachable  bool                      `json:"tokyo_reachable"`
	TokyoInputReady bool                      `json:"tokyo_input_ready"`
	ViewerAvailable bool                      `json:"viewer_available"`
	Platforms       map[string]platformOutput `json:"platforms"`
	Error           string                    `json:"error,omitempty"`
	CheckedAt       time.Time                 `json:"checked_at"`
}

type bananaLinkStatus struct {
	Name      string `json:"name"`
	Carrier   string `json:"carrier,omitempty"`
	Interface string `json:"interface,omitempty"`
	State     string `json:"state"`
	Online    bool   `json:"online"`
	Speedify  bool   `json:"speedify,omitempty"`
	SignalDBM *int   `json:"signal_dbm,omitempty"`
	Message   string `json:"message,omitempty"`
}

type bananaSnapshot struct {
	Hostname      string                      `json:"hostname,omitempty"`
	UptimeSeconds int64                       `json:"uptime_seconds,omitempty"`
	Links         map[string]bananaLinkStatus `json:"links,omitempty"`
	Speedify      bananaLinkStatus            `json:"speedify"`
	WireGuard     bananaLinkStatus            `json:"wireguard"`
	TokyoReceiver bananaLinkStatus            `json:"tokyo_receiver"`
	Camera        bananaLinkStatus            `json:"camera"`
	CheckedAt     time.Time                   `json:"checked_at,omitempty"`
}

type bananaMonitorStatus struct {
	Configured bool            `json:"configured"`
	Reachable  bool            `json:"reachable"`
	Status     *bananaSnapshot `json:"status,omitempty"`
	Error      string          `json:"error,omitempty"`
	CheckedAt  time.Time       `json:"checked_at"`
}

func newMonitorController(cfg monitorConfig) *monitorController {
	cfg.RelayStatusURL = strings.TrimSpace(cfg.RelayStatusURL)
	cfg.BananaStatusURL = strings.TrimRight(strings.TrimSpace(cfg.BananaStatusURL), "/")
	cfg.ViewerURL = strings.TrimSpace(cfg.ViewerURL)
	return &monitorController{cfg: cfg, http: &http.Client{Timeout: 3 * time.Second}}
}

func defaultPlatforms() map[string]platformOutput {
	return map[string]platformOutput{
		"twitch":  {State: "unknown"},
		"kick":    {State: "unknown"},
		"youtube": {State: "unknown"},
	}
}

func (m *monitorController) fetchJSON(ctx context.Context, method, rawURL, token string, output any) error {
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("monitor returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func (m *monitorController) streamSnapshot(ctx context.Context) streamMonitorStatus {
	result := streamMonitorStatus{Platforms: defaultPlatforms(), ViewerAvailable: m.cfg.ViewerURL != "", CheckedAt: time.Now().UTC()}
	if m.cfg.RelayStatusURL == "" {
		result.Error = "Tokyo relay monitoring is not configured"
		return result
	}
	var relay relaySnapshot
	if err := m.fetchJSON(ctx, http.MethodGet, m.cfg.RelayStatusURL, "", &relay); err != nil {
		result.Error = "Tokyo relay is unavailable"
		return result
	}
	result.TokyoReachable = true
	result.TokyoInputReady = relay.InputReady
	for _, destination := range relay.Destinations {
		name := strings.ToLower(strings.TrimSpace(destination.Name))
		if _, exists := result.Platforms[name]; !exists {
			continue
		}
		result.Platforms[name] = platformOutput{
			Enabled: destination.Enabled, Online: destination.Enabled && destination.State == "running",
			State: destination.State, Restarts: destination.Restarts, LastError: destination.LastError,
		}
	}
	return result
}

func (m *monitorController) streamStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.streamSnapshot(r.Context()))
}

func (m *monitorController) bananaSnapshot(ctx context.Context) bananaMonitorStatus {
	result := bananaMonitorStatus{Configured: m.cfg.BananaStatusURL != "", CheckedAt: time.Now().UTC()}
	if !result.Configured {
		result.Error = "Banana monitoring is not configured"
		return result
	}
	var status bananaSnapshot
	if err := m.fetchJSON(ctx, http.MethodGet, m.cfg.BananaStatusURL+"/v1/status", m.cfg.BananaToken, &status); err != nil {
		result.Error = "Banana is powered off or unreachable"
		return result
	}
	result.Reachable = true
	result.Status = &status
	return result
}

func (m *monitorController) bananaStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.bananaSnapshot(r.Context()))
}

var allowedBananaRecovery = map[string]string{
	"refresh-links":     "Refresh cellular links",
	"restart-speedify":  "Restart Speedify",
	"restart-wireguard": "Restart WireGuard",
	"reapply-routing":   "Reapply camera routing",
}

func (m *monitorController) bananaRecovery(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	label, allowed := allowedBananaRecovery[action]
	if !allowed {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown recovery action"})
		return
	}
	if r.Header.Get("X-OBS-Control") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "missing controller confirmation"})
		return
	}
	if m.cfg.BananaStatusURL == "" || m.cfg.BananaToken == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Banana recovery is not configured"})
		return
	}
	var response map[string]any
	err := m.fetchJSON(r.Context(), http.MethodPost, m.cfg.BananaStatusURL+"/v1/recovery/"+url.PathEscape(action), m.cfg.BananaToken, &response)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Banana recovery failed"})
		return
	}
	if ok, _ := response["ok"].(bool); !ok {
		message, _ := response["error"].(string)
		if message == "" {
			message = "Banana recovery was rejected"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": message})
		return
	}
	response["message"] = label + " requested"
	writeJSON(w, http.StatusAccepted, response)
}

func (m *monitorController) viewerHandler() http.Handler {
	if m.cfg.ViewerURL == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `<!doctype html><meta name="viewport" content="width=device-width"><style>html,body{height:100%;margin:0;background:#0b0810;color:#bba9c5;font:16px system-ui;display:grid;place-items:center;text-align:center}p{max-width:18rem;padding:1rem}</style><p>Tokyo viewer is waiting for its private feed.</p>`)
		})
	}
	target, err := url.Parse(m.cfg.ViewerURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Tokyo viewer URL is invalid"})
		})
	}
	basePath := strings.TrimRight(target.Path, "/") + "/"
	proxy := httputil.NewSingleHostReverseProxy(target)
	if m.http.Transport != nil {
		proxy.Transport = m.http.Transport
	}
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		incomingPath := strings.TrimPrefix(request.URL.Path, "/viewer/")
		originalDirector(request)
		request.URL.Path = basePath + incomingPath
		request.Host = target.Host
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Set("Cache-Control", "no-store")
		response.Header.Set("X-Frame-Options", "SAMEORIGIN")
		response.Header.Del("Content-Security-Policy")
		if location := response.Header.Get("Location"); location != "" {
			if parsed, err := url.Parse(location); err == nil {
				for _, prefix := range []string{"/pc/stream/", basePath} {
					if strings.HasPrefix(parsed.Path, prefix) {
						parsed.Scheme = ""
						parsed.Host = ""
						parsed.Path = "/viewer/" + strings.TrimPrefix(parsed.Path, prefix)
						response.Header.Set("Location", parsed.String())
						break
					}
				}
			}
		}
		cookies := response.Header.Values("Set-Cookie")
		if len(cookies) > 0 {
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				cookie = strings.ReplaceAll(cookie, "; Secure", "")
				cookie = strings.ReplaceAll(cookie, "; SameSite=None", "; SameSite=Lax")
				cookie = strings.ReplaceAll(cookie, "; Partitioned", "")
				response.Header.Add("Set-Cookie", cookie)
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<style>html,body{height:100%;margin:0;background:#0b0810;color:#bba9c5;font:16px system-ui;display:grid;place-items:center}</style><p>Tokyo feed is offline.</p>`)
	}
	return proxy
}

func validateMonitorConfig(cfg monitorConfig) error {
	for name, raw := range map[string]string{"relay status": cfg.RelayStatusURL, "banana status": cfg.BananaStatusURL, "viewer": cfg.ViewerURL} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid %s URL", name)
		}
	}
	if cfg.BananaToken != "" && cfg.BananaStatusURL == "" {
		return errors.New("banana token is configured without a status URL")
	}
	return nil
}
