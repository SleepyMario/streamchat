package gui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientruntime"
)

//go:embed web/*
var embedded embed.FS

type Config struct {
	Listen, Password string
	Runtime          *clientruntime.Runtime
	Shutdown         func()
}
type event struct {
	name    string
	payload []byte
}
type Server struct {
	cfg         Config
	web         fs.FS
	mu          sync.Mutex
	subscribers map[chan event]struct{}
	history     [][]byte
}

func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8792"
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("invalid GUI listen address: %w", err)
	}
	if !loopback(host) && cfg.Password == "" {
		return nil, errors.New("a GUI password is required for a non-loopback listen address")
	}
	web, err := fs.Sub(embedded, "web")
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, web: web, subscribers: map[chan event]struct{}{}}
	if cfg.Runtime != nil {
		cfg.Runtime.Subscribe(func(state clientruntime.State) { _ = s.publishEvent("state", state, false) })
	}
	return s, nil
}
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (s *Server) URL() string {
	host, port, _ := net.SplitHostPort(s.cfg.Listen)
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func (s *Server) Publish(message chat.Message) error {
	if err := message.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, payload)
	if len(s.history) > 500 {
		s.history = append([][]byte(nil), s.history[len(s.history)-500:]...)
	}
	for subscriber := range s.subscribers {
		select {
		case subscriber <- event{name: "message", payload: payload}:
		default:
			delete(s.subscribers, subscriber)
			close(subscriber)
		}
	}
	return nil
}

func (s *Server) publishEvent(name string, value any, retain bool) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if retain && name == "message" {
		s.history = append(s.history, payload)
		if len(s.history) > 500 {
			s.history = append([][]byte(nil), s.history[len(s.history)-500:]...)
		}
	}
	for subscriber := range s.subscribers {
		select {
		case subscriber <- event{name: name, payload: payload}:
		default:
			delete(s.subscribers, subscriber)
			close(subscriber)
		}
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/action", s.action)
	mux.HandleFunc("/api/config", s.configHealth)
	mux.HandleFunc("/api/setup", s.setup)
	mux.HandleFunc("/api/archive", s.archive)
	mux.HandleFunc("/api/open", s.open)
	mux.HandleFunc("/api/shutdown", s.shutdown)
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return headers(s.authenticate(mux))
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.cfg.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Streamchat GUI", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self'; script-src 'self'; connect-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	subscriber := make(chan event, 128)
	s.mu.Lock()
	s.subscribers[subscriber] = struct{}{}
	history := append([][]byte(nil), s.history...)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, ok := s.subscribers[subscriber]; ok {
			delete(s.subscribers, subscriber)
			close(subscriber)
		}
		s.mu.Unlock()
	}()
	_, _ = io.WriteString(w, "event: status\ndata: {\"connected\":true}\n\n")
	if s.cfg.Runtime != nil {
		if payload, err := json.Marshal(s.cfg.Runtime.State()); err == nil {
			_, _ = fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload)
		}
	}
	for _, payload := range history {
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
	}
	flusher.Flush()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-subscriber:
			if !open {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.payload)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) requireRuntime(w http.ResponseWriter) *clientruntime.Runtime {
	if s.cfg.Runtime == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable in demo mode"})
		return nil
	}
	return s.cfg.Runtime
}

func method(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Method == expected {
		return true
	}
	w.Header().Set("Allow", expected)
	jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	return false
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	jsonResponse(w, http.StatusOK, runtime.RefreshStatus(ctx))
}

type actionRequest struct {
	Action, Platform, Text, User, Duration, Title, Category string
	Days                                                    int
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var request actionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		jsonResponse(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var command string
	switch strings.ToLower(request.Action) {
	case "select":
		command = "/" + request.Platform
	case "send":
		command = request.Text
	case "title":
		command = "/title " + request.Title
	case "category":
		command = "/category " + request.Category
	case "ban":
		command = "/ban " + request.Platform + " " + request.User
	case "timeout":
		command = "/timeout " + request.Platform + " " + request.User + " " + request.Duration
	case "clear":
		command = "/clear " + request.Platform
		if request.Days > 0 {
			command += fmt.Sprintf(" %d", request.Days)
		}
	default:
		jsonResponse(w, 400, map[string]string{"error": "unknown action"})
		return
	}
	result, err := runtime.Execute(ctx, command)
	if err != nil {
		jsonResponse(w, 422, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "result": result, "state": runtime.State()})
}
func (s *Server) configHealth(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	jsonResponse(w, 200, runtime.ConfigHealth())
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	jsonResponse(w, 200, runtime.SetupGuides())
}
func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	stats, err := runtime.ArchiveStats(ctx)
	if err != nil {
		jsonResponse(w, 422, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, stats)
}
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	runtime := s.requireRuntime(w)
	if runtime == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var request struct {
		Platform string `json:"platform"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		jsonResponse(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	target, err := runtime.OpenURL(ctx, request.Platform)
	if err != nil {
		jsonResponse(w, 422, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]string{"url": target})
}
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if s.cfg.Shutdown == nil {
		jsonResponse(w, 503, map[string]string{"error": "shutdown unavailable"})
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
	go s.cfg.Shutdown()
}
func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
