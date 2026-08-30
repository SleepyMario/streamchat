package botgui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/SleepyMario/streamchat/internal/bot"
)

//go:embed web/*
var embedded embed.FS

type Config struct {
	Listen, Password string
	Engine           *bot.Engine
	Save             func(bot.State) error
	Accounts         func() map[string]string
	Stream           func() any
	Channel          func() any
	Chat             func() any
}

type Server struct {
	cfg    Config
	web    fs.FS
	server *http.Server
}

func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		return nil, errors.New("bot GUI listen address is required")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("invalid bot GUI listen address: %w", err)
	}
	if !loopback(host) && cfg.Password == "" {
		return nil, errors.New("a bot GUI password is required for a non-loopback listen address")
	}
	web, err := fs.Sub(embedded, "web")
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, web: web}, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/settings", s.settings)
	mux.HandleFunc("/api/test", s.test)
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return headers(s.authenticate(mux))
}

func (s *Server) Run() error {
	s.server = &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown() error {
	if s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.cfg.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Streamchat Bot"`)
			http.Error(w, "authentication required", 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	state := s.cfg.Engine.State()
	accounts := map[string]string{}
	if s.cfg.Accounts != nil {
		accounts = s.cfg.Accounts()
	}
	var stream any
	if s.cfg.Stream != nil {
		stream = s.cfg.Stream()
	}
	var channel any
	if s.cfg.Channel != nil {
		channel = s.cfg.Channel()
	}
	var recentChat any
	if state.ShowChat && s.cfg.Chat != nil {
		recentChat = s.cfg.Chat()
	}
	jsonResponse(w, 200, map[string]any{"bot": state, "accounts": accounts, "activity": s.cfg.Engine.Activity(), "stream": stream, "channel": channel, "recent_chat": recentChat})
}

func (s *Server) test(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var request struct {
		Platform string `json:"platform"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid test request"})
		return
	}
	if err := s.cfg.Engine.Test(r.Context(), request.Platform); err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]string{"error": "test reply failed"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "sent"})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonResponse(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var state bot.State
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&state); err != nil {
		jsonResponse(w, 400, map[string]string{"error": "invalid settings"})
		return
	}
	if state.Cooldown < 1 || state.Cooldown > 3600 || state.CommandsReply == "" {
		jsonResponse(w, 400, map[string]string{"error": "reply is required and cooldown must be 1–3600 seconds"})
		return
	}
	if s.cfg.Save != nil {
		if err := s.cfg.Save(state); err != nil {
			jsonResponse(w, 500, map[string]string{"error": "could not save settings"})
			return
		}
	}
	s.cfg.Engine.Update(state)
	jsonResponse(w, 200, map[string]any{"bot": s.cfg.Engine.State()})
}
