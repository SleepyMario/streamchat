// streamchat-obs-agent provides desktop-local Windows sources for Streamchat.
package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed cava.html
var cavaPage []byte

type config struct{ Listen, TokenFile, OBSURL, OBSPasswordFile, BotURL, BotPasswordFile, Root, FFmpeg, Cava, CavaConfig, Microphones string }

var agentConfig config
var client = &http.Client{Timeout: 30 * time.Second}

type subtitleConfig struct{ Source, OriginalOutput, EnglishOutput, ChineseOutput, CombinedOutput string }
type lease struct {
	State     string `json:"state"`
	WorkerURL string `json:"worker_url"`
	Token     string `json:"token"`
}
type agent struct {
	mu             sync.Mutex
	token          string
	obs            *obsClient
	sub            subtitleConfig
	cancel         context.CancelFunc
	done           chan struct{}
	state, message string
	gps            string
	live           bool
	levels         []float64
	cavaAt         time.Time
}

func readSecret(path string) (string, error) {
	b, e := os.ReadFile(path)
	return strings.TrimSpace(string(b)), e
}
func (a *agent) bot(ctx context.Context, method, path string, result any) error {
	password, e := readSecret(agentConfig.BotPasswordFile)
	if e != nil {
		return errors.New("subtitle backend credential unavailable")
	}
	req, e := http.NewRequestWithContext(ctx, method, agentConfig.BotURL+path, nil)
	if e != nil {
		return e
	}
	req.SetBasicAuth("streamchat", password)
	res, e := client.Do(req)
	if e != nil {
		return errors.New("subtitle backend unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("subtitle backend returned HTTP %d", res.StatusCode)
	}
	if result == nil {
		_, e = io.Copy(io.Discard, res.Body)
		return e
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(result)
}
func (a *agent) setState(s, m string) { a.mu.Lock(); a.state = s; a.message = m; a.mu.Unlock() }
func (a *agent) runSubtitles(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer clearSubtitleOutputs(a.sub)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		if e := a.bot(stopCtx, "POST", "/api/subtitles/stop", nil); e != nil {
			a.setState("error", "Subtitle worker cleanup failed; inspect Tokyo")
		}
	}()
	var l lease
	if e := a.bot(ctx, "POST", "/api/subtitles/start", &l); e != nil {
		a.setState("error", e.Error())
		return
	}
	readyDeadline := time.Now().Add(15 * time.Minute)
	for l.State != "ready" {
		if l.State == "error" || l.State == "stopped" || l.State == "idle" || time.Now().After(readyDeadline) {
			a.setState("error", "Subtitle worker did not become ready")
			return
		}
		select {
		case <-ctx.Done():
			a.setState("off", "Subtitles off")
			return
		case <-time.After(5 * time.Second):
		}
		if e := a.bot(ctx, "POST", "/api/subtitles/heartbeat", &l); e != nil {
			a.setState("error", e.Error())
			return
		}
	}
	if e := a.bot(ctx, "GET", "/api/subtitles/lease", &l); e != nil {
		a.setState("error", e.Error())
		return
	}
	if l.Token == "" || l.WorkerURL == "" {
		a.setState("error", "Subtitle worker lease incomplete")
		return
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := make(chan error, 1)
	go func() {
		sender <- retrySubtitleSender(streamCtx, 3*time.Second, func() error {
			return runSubtitleSender(streamCtx, a.sub, l.WorkerURL, l.Token, func() { a.setState("running", "Microphone subtitles active") })
		}, a.setState)
	}()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-sender
			a.setState("off", "Subtitles off")
			return
		case e := <-sender:
			if e != nil {
				a.setState("error", "Microphone/worker connection stopped; reconnect microphone and retry")
			} else {
				a.setState("off", "Subtitles finished")
			}
			return
		case <-tick.C:
			if e := a.bot(ctx, "POST", "/api/subtitles/heartbeat", nil); e != nil {
				cancel()
				<-sender
				a.setState("error", e.Error())
				return
			}
		}
	}
}
func (a *agent) start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return errors.New("subtitle session already active")
		}
	}
	if _, e := resolveSubtitleSource(ctx, a.sub.Source); e != nil {
		return e
	}
	task, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.done = make(chan struct{})
	a.state = "starting"
	a.message = "Starting GPU subtitles"
	go a.runSubtitles(task, a.done)
	return nil
}
func (a *agent) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if (r.Method == "GET" && r.URL.Path == "/obs") || (r.Method == "POST" && (r.URL.Path == "/obs/start" || r.URL.Path == "/obs/stop")) {
		a.lifecycle(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/chat" {
		var feed struct {
			RecentChat []struct {
				ID        string `json:"id"`
				Platform  string `json:"platform"`
				Timestamp string `json:"timestamp"`
				Author    string `json:"author_display_name"`
				Text      string `json:"text"`
				EventType string `json:"event_type"`
			} `json:"recent_chat"`
		}
		if e := a.bot(r.Context(), "GET", "/api/overlay/chat", &feed); e != nil {
			http.Error(w, "Chat backend unavailable", 502)
			return
		}
		json.NewEncoder(w).Encode(feed)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/status" {
		a.mu.Lock()
		s := map[string]any{"subtitles": a.state, "message": a.message, "irl_live": a.live, "gps": a.gps, "cava_frames_recent": time.Since(a.cavaAt) < 3*time.Second}
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	switch r.URL.Path {
	case "/subtitles/start":
		if e := a.start(r.Context()); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
	case "/subtitles/stop":
		a.mu.Lock()
		if a.cancel != nil {
			a.cancel()
		}
		a.mu.Unlock()
	case "/gps":
		var in struct {
			Layout string `json:"layout"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) != nil || !validLayout(in.Layout) {
			http.Error(w, "invalid GPS layout", 400)
			return
		}
		a.mu.Lock()
		a.gps = in.Layout
		a.mu.Unlock()
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
func validLayout(v string) bool {
	return v == "Dashboard" || v == "Minimal" || v == "Full" || v == "Off"
}
func (a *agent) item(name string, enabled bool) error {
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	b, e := a.obs.request(ctx, "GetSceneItemId", map[string]string{"sceneName": "IRL - VPS", "sourceName": name})
	if e != nil {
		return e
	}
	var d struct {
		ID int `json:"sceneItemId"`
	}
	if e = json.Unmarshal(b, &d); e != nil {
		return e
	}
	_, e = a.obs.request(ctx, "SetSceneItemEnabled", map[string]any{"sceneName": "IRL - VPS", "sceneItemId": d.ID, "sceneItemEnabled": enabled})
	return e
}
func (a *agent) irl() {
	h := &http.Client{Timeout: 2 * time.Second}
	var previous string
	var synced time.Time
	for {
		time.Sleep(2 * time.Second)
		ready := false
		if r, e := h.Get("http://127.0.0.1:9997/v3/paths/get/irl"); e == nil {
			var p struct {
				Ready bool `json:"ready"`
			}
			if r.StatusCode == 200 {
				_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&p)
				ready = p.Ready
			}
			r.Body.Close()
		}
		a.mu.Lock()
		gps := a.gps
		a.live = ready
		a.mu.Unlock()
		key := fmt.Sprint(ready) + gps
		if key == previous && time.Since(synced) < 15*time.Second {
			continue
		}
		if e := a.item("VPS-MMTX", ready); e != nil {
			continue
		}
		ok := true
		for _, layout := range []string{"Dashboard", "Minimal", "Full"} {
			if e := a.item("IRL GPS "+layout, ready && gps == layout); e != nil {
				ok = false
			}
		}
		if ok {
			previous = key
			synced = time.Now()
		}
	}
}
func (a *agent) cava() {
	for {
		cmd := exec.Command(agentConfig.Cava, "-p", agentConfig.CavaConfig)
		hideWindow(cmd)
		cmd.Stderr = os.Stderr
		pipe, e := cmd.StdoutPipe()
		if e == nil {
			e = cmd.Start()
		}
		if e == nil {
			scan := bufio.NewScanner(pipe)
			for scan.Scan() {
				parts := strings.Split(strings.TrimSpace(scan.Text()), ";")
				values := []float64{}
				for _, part := range parts {
					if part == "" {
						continue
					}
					v, e := strconv.Atoi(part)
					if e != nil {
						values = nil
						break
					}
					values = append(values, min(1, max(0, float64(v)/1000)))
				}
				if len(values) == 48 {
					a.mu.Lock()
					a.levels = values
					a.cavaAt = time.Now()
					a.mu.Unlock()
				}
			}
			if e := cmd.Wait(); e != nil {
				log.Printf("CAVA exited: %v", e)
			}
		}
		if e != nil {
			log.Printf("CAVA start: %v", e)
		}
		a.mu.Lock()
		a.levels = make([]float64, 48)
		a.mu.Unlock()
		time.Sleep(3 * time.Second)
	}
}
func main() {
	path := flag.String("config", "", "private desktop-agent config")
	flag.Parse()
	b, e := os.ReadFile(*path)
	if e != nil {
		log.Fatal("config unavailable")
	}
	if json.Unmarshal(b, &agentConfig) != nil {
		log.Fatal("invalid config")
	}
	logFile, err := os.OpenFile(filepath.Join(agentConfig.Root, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal("log unavailable")
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	os.Stderr = logFile
	token, e := readSecret(agentConfig.TokenFile)
	if e != nil || len(token) < 32 {
		log.Fatal("agent token unavailable")
	}
	a := &agent{token: token, obs: &obsClient{url: agentConfig.OBSURL, configPath: agentConfig.OBSPasswordFile, timeout: 5 * time.Second}, state: "off", message: "Subtitles off", gps: "Dashboard", levels: make([]float64, 48)}
	root := filepath.Join(agentConfig.Root, "subtitles")
	a.sub = subtitleConfig{agentConfig.Microphones, filepath.Join(root, "original.txt"), filepath.Join(root, "english.txt"), filepath.Join(root, "chinese.txt"), filepath.Join(root, "current.txt")}
	clearSubtitleOutputs(a.sub)
	gps := http.FileServer(http.Dir(filepath.Join(agentConfig.Root, "gps")))
	go func() { log.Fatal(http.ListenAndServe("127.0.0.1:8765", gps)) }()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(cavaPage)
	})
	mux.HandleFunc("/levels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		a.mu.Lock()
		defer a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(a.levels)
	})
	go func() { log.Fatal(http.ListenAndServe("127.0.0.1:8796", mux)) }()
	go a.cava()
	go a.irl()
	srv := http.Server{Addr: agentConfig.Listen, Handler: http.HandlerFunc(a.api), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
