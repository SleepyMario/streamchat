package emote

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testPNG = []byte("\x89PNG\r\n\x1a\nstreamchat")

func TestCachePathSafetyAndXDGLocation(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/example-cache")
	directory, err := DefaultCacheDirectory()
	if err != nil || directory != "/tmp/example-cache/streamchat/emotes" {
		t.Fatalf("directory=%q err=%v", directory, err)
	}
	path, err := CachePath(directory, "kick", "4147888")
	if err != nil || path != filepath.Join(directory, "kick", "4147888.img") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, unsafe := range []string{"../kick", "7/../../secret", "", "name.png"} {
		if _, err = CachePath(directory, "kick", unsafe); err == nil {
			t.Fatalf("accepted unsafe key %q", unsafe)
		}
	}
}

func TestCacheDownloadIsAsynchronousDeduplicatedAndPersistent(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-release
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	cache, err := NewCache(CacheOptions{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	if _, ok := cache.Resolve("kick", "7", server.URL, func(err error) { done <- err }); ok {
		t.Fatal("uncached image reported as hit")
	}
	if _, ok := cache.Resolve("kick", "7", server.URL, nil); ok {
		t.Fatal("pending image reported as hit")
	}
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download did not complete")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
	path, ok := cache.Resolve("kick", "7", server.URL, nil)
	if !ok || !strings.HasSuffix(path, filepath.Join("kick", "7.img")) {
		t.Fatalf("cache hit path=%q ok=%v", path, ok)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != string(testPNG) {
		t.Fatalf("cached data=%q err=%v", data, readErr)
	}
}

func TestCacheFailureAndOversizeStayTextual(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		code int
		max  int64
	}{
		{name: "provider failure", code: http.StatusBadGateway, body: []byte("secret provider response")},
		{name: "oversize", code: http.StatusOK, body: append(testPNG, make([]byte, 64)...), max: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(tt.code)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()
			cache, err := NewCache(CacheOptions{Directory: t.TempDir(), MaxBytes: tt.max})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			if _, ok := cache.Resolve("kick", "8", server.URL, func(err error) { done <- err }); ok {
				t.Fatal("failed asset reported as hit")
			}
			select {
			case err = <-done:
				if err == nil || strings.Contains(err.Error(), "secret provider response") {
					t.Fatalf("unsanitized or missing error: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("download did not complete")
			}
		})
	}
}

func TestOptionalDiagnosticsUsePrivateCacheLog(t *testing.T) {
	directory := t.TempDir()
	log, err := newDiagnosticLog(directory)
	if err != nil {
		t.Fatal(err)
	}
	log.write("download failed: provider=kick id=1730755 error=HTTP 429")
	if err = log.close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "debug.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "provider=kick id=1730755") {
		t.Fatalf("data=%q err=%v", data, err)
	}
}
