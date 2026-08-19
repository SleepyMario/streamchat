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

	"github.com/SleepyMario/streamchat/internal/chat"
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
	twitchPath, err := CachePath(directory, "twitch", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49")
	if err != nil || twitchPath != filepath.Join(directory, "twitch", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49.img") || filepath.Dir(twitchPath) == filepath.Dir(path) {
		t.Fatalf("Twitch cache path=%q Kick path=%q err=%v", twitchPath, path, err)
	}
	for _, unsafe := range []string{"../kick", "7/../../secret", "", "name.png"} {
		if _, err = CachePath(directory, "kick", unsafe); err == nil {
			t.Fatalf("accepted unsafe key %q", unsafe)
		}
	}
}

func TestTwitchCacheDownloadAndHitStayProviderSeparated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	directory := t.TempDir()
	cache, err := NewCache(CacheOptions{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	if path, ok := cache.Resolve("twitch", "25", server.URL, func(err error) { done <- err }); ok || path != "" {
		t.Fatalf("uncached path=%q ok=%v", path, ok)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Twitch emote download did not complete")
	}
	path, ok := cache.Resolve("twitch", "25", server.URL, nil)
	if !ok || path != filepath.Join(directory, "twitch", "25.img") {
		t.Fatalf("cache hit path=%q ok=%v", path, ok)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "kick", "25.img")); !os.IsNotExist(statErr) {
		t.Fatalf("Twitch asset leaked into Kick cache: %v", statErr)
	}
}

func TestTwitchCacheFailureKeepsNameAndDoesNotBackoffKick(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/twitch" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	cache, err := NewCache(CacheOptions{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	cache.Resolve("twitch", "25", server.URL+"/twitch", func(err error) { failed <- err })
	select {
	case err = <-failed:
		if err == nil {
			t.Fatal("Twitch cache failure was reported as success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Twitch cache failure did not complete")
	}
	item := chat.Emote{ID: "25", Name: "Kappa", URL: server.URL + "/twitch", Start: 0, End: 4}
	line := FormatText(chat.PlatformTwitch, "Kappa", []chat.Emote{item}, nil, func(platform chat.Platform, item chat.Emote) (string, bool) {
		return cache.Resolve(string(platform), item.ID, item.URL, nil)
	})
	if line.Text != "Kappa" || line.GraphicalText != "" || len(line.Images) != 0 {
		t.Fatalf("Twitch failure fallback=%+v", line)
	}
	kickDone := make(chan error, 1)
	cache.Resolve("kick", "25", server.URL+"/kick", func(err error) { kickDone <- err })
	select {
	case err = <-kickDone:
		if err != nil {
			t.Fatalf("Twitch failure affected Kick: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Kick cache request did not complete")
	}
	if path, ok := cache.Resolve("kick", "25", server.URL+"/kick", nil); !ok || !strings.HasSuffix(path, filepath.Join("kick", "25.img")) {
		t.Fatalf("Kick cache hit path=%q ok=%v", path, ok)
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
