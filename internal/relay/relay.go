package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/gorilla/websocket"
)

const maxMessageBytes = 1 << 20

type peer struct {
	conn *websocket.Conn
	send chan []byte
}

type hub struct {
	mu      sync.Mutex
	clients map[*peer]struct{}
}

func newHub() *hub { return &hub{clients: make(map[*peer]struct{})} }

func (h *hub) add(p *peer) {
	h.mu.Lock()
	h.clients[p] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(p *peer) {
	h.mu.Lock()
	if _, ok := h.clients[p]; ok {
		delete(h.clients, p)
		close(p.send)
	}
	h.mu.Unlock()
	_ = p.conn.Close()
}

func (h *hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.clients {
		select {
		case p.send <- payload:
		default:
			delete(h.clients, p)
			close(p.send)
			_ = p.conn.Close()
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.clients {
		delete(h.clients, p)
		close(p.send)
		_ = p.conn.Close()
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Server combines an existing webhook handler with an authenticated WebSocket
// endpoint and forwards normalized messages.
type Server struct {
	Listen, Path, Token string
	Webhook             http.Handler
	// Accept runs before broadcast. Returning false suppresses a duplicate;
	// returning an error stops the server so archival failures are visible.
	Accept func(context.Context, chat.Message) (bool, error)
	hub    *hub
	server *http.Server
}

func NewServer(listen, path, token string, webhook http.Handler) *Server {
	return &Server{Listen: listen, Path: path, Token: token, Webhook: webhook, hub: newHub()}
}

func authorized(header, token string) bool {
	if token == "" {
		return false
	}
	want := []byte("Bearer " + token)
	got := []byte(header)
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorized(r.Header.Get("Authorization"), s.Token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(4096)
	p := &peer{conn: conn, send: make(chan []byte, 64)}
	s.hub.add(p)
	defer s.hub.remove(p)
	go func() {
		for payload := range p.send {
			if err := p.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				_ = p.conn.Close()
				return
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.Path, s.websocket)
	if s.Webhook != nil {
		mux.Handle("/", s.Webhook)
	}
	return mux
}

func (s *Server) Broadcast(m chat.Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.hub.broadcast(payload)
	return nil
}

func (s *Server) forward(ctx context.Context, messages <-chan chat.Message) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-messages:
			if !ok {
				return errors.New("server ingestion message stream closed")
			}
			if s.Accept != nil {
				accepted, err := s.Accept(ctx, m)
				if err != nil {
					return fmt.Errorf("persist message before relay: %w", err)
				}
				if !accepted {
					continue
				}
			}
			if err := s.Broadcast(m); err != nil {
				return fmt.Errorf("relay message: %w", err)
			}
		}
	}
}

func (s *Server) Run(ctx context.Context, messages <-chan chat.Message) error {
	if s.Token == "" {
		return errors.New("relay authentication token is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.server = &http.Server{
		Addr:              s.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	forwardErr := make(chan error, 1)
	httpErr := make(chan error, 1)
	go func() { forwardErr <- s.forward(runCtx, messages) }()
	go func() { httpErr <- s.server.ListenAndServe() }()
	var result error
	select {
	case <-ctx.Done():
	case err := <-forwardErr:
		result = err
	case err := <-httpErr:
		if errors.Is(err, http.ErrServerClosed) {
			result = nil
		} else {
			result = err
		}
	}
	cancel()
	s.hub.closeAll()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = s.server.Shutdown(shutdownCtx)
	return result
}

type DialFunc func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)

// Client is a chat adapter that reconnects to a Streamchat relay and emits
// normalized messages into the existing aggregation/rendering pipeline.
type Client struct {
	URL, Token string
	Dial       DialFunc
	RetryDelay time.Duration
}

func NewClient(url, token string) *Client {
	return &Client{URL: url, Token: token, Dial: websocket.DefaultDialer.DialContext, RetryDelay: time.Second}
}

func (c *Client) Name() string { return "server" }

func (c *Client) read(ctx context.Context, out chan<- chat.Message) error {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+c.Token)
	conn, response, err := c.Dial(ctx, c.URL, header)
	if response != nil {
		defer response.Body.Close()
	}
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return &chat.AdapterError{Kind: chat.Authentication, Op: "Streamchat relay", Err: errors.New("authorization rejected")}
		}
		return &chat.AdapterError{Kind: chat.Recoverable, Op: "Streamchat relay", Err: errors.New("connection failed")}
	}
	defer conn.Close()
	conn.SetReadLimit(maxMessageBytes)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var m chat.Message
		if err = json.Unmarshal(payload, &m); err != nil {
			return errors.New("relay sent an invalid message")
		}
		if err = m.Validate(); err != nil {
			return errors.New("relay sent an invalid message")
		}
		select {
		case out <- m:
		case <-ctx.Done():
			return nil
		}
	}
}

func (c *Client) Run(ctx context.Context, out chan<- chat.Message) error {
	if c.Token == "" {
		return &chat.AdapterError{Kind: chat.Authentication, Op: "Streamchat relay", Err: errors.New("authentication token is required")}
	}
	for {
		err := c.read(ctx, out)
		if ctx.Err() != nil {
			return nil
		}
		var adapterErr *chat.AdapterError
		if errors.As(err, &adapterErr) && adapterErr.Kind == chat.Authentication {
			return err
		}
		timer := time.NewTimer(c.RetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
