package streamprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State describes the video stream that actually reached the relay host.
type State struct {
	Online              bool      `json:"online"`
	Width               int       `json:"width,omitempty"`
	Height              int       `json:"height,omitempty"`
	Bitrate             int64     `json:"bitrate_bps,omitempty"`
	ConnectionAvailable bool      `json:"connection_available"`
	ConnectionBPS       int64     `json:"connection_bps,omitempty"`
	CheckedAt           time.Time `json:"checked_at"`
}

type Probe struct {
	ffprobe, input        string
	metricsURL, mediaPath string
	httpClient            *http.Client
	interval              time.Duration
	mu                    sync.RWMutex
	state                 State
	previousInbound       uint64
	previousAt            time.Time
}

func New(ffprobe, input string, interval time.Duration) *Probe {
	if ffprobe == "" {
		ffprobe = "/usr/bin/ffprobe"
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Probe{ffprobe: ffprobe, input: input, interval: interval, httpClient: &http.Client{Timeout: 3 * time.Second}}
}

// WithMetrics enables stream-only traffic measurement from MediaMTX's
// Prometheus-compatible metrics endpoint.
func (p *Probe) WithMetrics(metricsURL, mediaPath string) *Probe {
	p.metricsURL = strings.TrimSpace(metricsURL)
	p.mediaPath = strings.TrimSpace(mediaPath)
	return p
}

func (p *Probe) Run(ctx context.Context) {
	p.check(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.check(ctx)
		}
	}
}

func (p *Probe) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *Probe) check(parent context.Context) {
	state := State{CheckedAt: time.Now()}
	if p.input != "" {
		ctx, cancel := context.WithTimeout(parent, 4*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, p.ffprobe,
			"-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height,bit_rate", "-of", "json", p.input,
		).Output()
		var result struct {
			Streams []struct {
				Width, Height int
				Bitrate       json.Number `json:"bit_rate"`
			} `json:"streams"`
		}
		if err == nil && json.Unmarshal(out, &result) == nil && len(result.Streams) > 0 && result.Streams[0].Width > 0 && result.Streams[0].Height > 0 {
			state.Online = true
			state.Width = result.Streams[0].Width
			state.Height = result.Streams[0].Height
			state.Bitrate, _ = result.Streams[0].Bitrate.Int64()
		}
	}
	if inbound, ok := p.inboundBytes(parent); ok {
		now := time.Now()
		state.ConnectionAvailable = true
		if !p.previousAt.IsZero() && inbound >= p.previousInbound {
			seconds := now.Sub(p.previousAt).Seconds()
			if seconds > 0 {
				state.ConnectionBPS = int64(float64(inbound-p.previousInbound) * 8 / seconds)
			}
		}
		p.previousInbound = inbound
		p.previousAt = now
	}
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

func (p *Probe) inboundBytes(ctx context.Context) (uint64, bool) {
	if p.metricsURL == "" || p.mediaPath == "" {
		return 0, false
	}
	u, err := url.Parse(p.metricsURL)
	if err != nil {
		return 0, false
	}
	query := u.Query()
	query.Set("type", "paths")
	query.Set("path", p.mediaPath)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false
	}
	response, err := p.httpClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, false
	}
	value, err := parseInboundBytes(response.Body, p.mediaPath)
	return value, err == nil
}

func parseInboundBytes(r io.Reader, mediaPath string) (uint64, error) {
	wanted := `name="` + strings.ReplaceAll(strings.ReplaceAll(mediaPath, `\`, `\\`), `"`, `\"`) + `"`
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "paths_inbound_bytes{") || !strings.Contains(line, wanted) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || value < 0 {
			continue
		}
		return uint64(value), nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, io.EOF
}
