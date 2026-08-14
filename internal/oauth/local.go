package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Request struct {
	AuthorizeURL, ClientID, RedirectURI string
	Scopes                              []string
	UsePKCE                             bool
}

type Result struct{ Code, Verifier string }

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizationURL builds a CSRF-protected authorization-code request. It is
// separate from Listen so URL/scopes can be tested without opening sockets.
func AuthorizationURL(r Request, state, verifier string) (string, error) {
	u, err := url.Parse(r.AuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", r.ClientID)
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("scope", strings.Join(r.Scopes, " "))
	q.Set("state", state)
	if r.UsePKCE {
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func Authorize(ctx context.Context, r Request, out io.Writer, open func(string) error) (Result, error) {
	state, err := randomURLSafe(32)
	if err != nil {
		return Result{}, err
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		return Result{}, err
	}
	authURL, err := AuthorizationURL(r, state, verifier)
	if err != nil {
		return Result{}, err
	}
	u, err := url.Parse(r.RedirectURI)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return Result{}, errors.New("redirect URI must be an absolute HTTP loopback URL")
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return Result{}, errors.New("redirect URI must include a port")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return Result{}, errors.New("redirect URI must use localhost or a loopback IP")
		}
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return Result{}, fmt.Errorf("start OAuth callback on %s: %w", u.Host, err)
	}
	defer ln.Close()
	type callback struct{ code, state, oauthErr string }
	ch := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != u.Path {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		v := callback{code: req.URL.Query().Get("code"), state: req.URL.Query().Get("state"), oauthErr: req.URL.Query().Get("error")}
		if v.state != state {
			http.Error(w, "invalid OAuth state", http.StatusBadRequest)
			return
		}
		select {
		case ch <- v:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Streamchat</title><p>%s</p>", html.EscapeString("Authorization received. You may close this tab."))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	fmt.Fprintf(out, "Open this authorization URL in your browser:\n\n%s\n\n", authURL)
	if open == nil {
		open = OpenBrowser
	}
	if err := open(authURL); err != nil {
		fmt.Fprintln(out, "The browser could not be opened automatically; copy the URL above.")
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case v := <-ch:
		if v.oauthErr != "" {
			return Result{}, fmt.Errorf("authorization was not completed: %s", v.oauthErr)
		}
		if v.code == "" {
			return Result{}, errors.New("authorization callback did not contain a code")
		}
		return Result{Code: v.code, Verifier: verifier}, nil
	}
}

func OpenBrowser(u string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		args = []string{u}
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", u}
	default:
		name = "xdg-open"
		args = []string{u}
	}
	return exec.Command(name, args...).Start()
}
