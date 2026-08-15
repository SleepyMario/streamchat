package emote

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxImageBytes = int64(2 << 20)
	defaultHTTPTimeout   = 10 * time.Second
	defaultRetryAfter    = 5 * time.Minute
)

type CacheOptions struct {
	Directory  string
	HTTP       *http.Client
	MaxBytes   int64
	RetryAfter time.Duration
	Now        func() time.Time
}

type Cache struct {
	directory  string
	http       *http.Client
	maxBytes   int64
	retryAfter time.Duration
	now        func() time.Time
	mu         sync.Mutex
	pending    map[string]func(error)
	failed     map[string]time.Time
}

type CacheState string

const (
	CacheInvalid CacheState = "invalid"
	CacheHit     CacheState = "hit"
	CachePending CacheState = "pending"
	CacheBackoff CacheState = "backoff"
	CacheQueued  CacheState = "queued"
)

func NewCache(options CacheOptions) (*Cache, error) {
	directory := options.Directory
	if directory == "" {
		var err error
		directory, err = DefaultCacheDirectory()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create emote cache: %w", err)
	}
	_ = os.Chmod(directory, 0700)
	client := options.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxImageBytes
	}
	retryAfter := options.RetryAfter
	if retryAfter <= 0 {
		retryAfter = defaultRetryAfter
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Cache{directory: directory, http: client, maxBytes: maxBytes, retryAfter: retryAfter, now: now, pending: make(map[string]func(error)), failed: make(map[string]time.Time)}, nil
}

func DefaultCacheDirectory() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("locate home directory for emote cache")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "streamchat", "emotes"), nil
}

func CachePath(root, provider, id string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	id = strings.TrimSpace(id)
	if !safeKey(provider) || !safeKey(id) {
		return "", errors.New("invalid emote cache key")
	}
	return filepath.Join(root, provider, id+".img"), nil
}

func safeKey(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// Resolve returns a persistent cache hit immediately or starts one
// deduplicated asynchronous download and returns a textual-fallback miss.
func (c *Cache) Resolve(provider, id, rawURL string, done func(error)) (string, bool) {
	path, ok, _ := c.ResolveDetailed(provider, id, rawURL, done)
	return path, ok
}

func (c *Cache) ResolveDetailed(provider, id, rawURL string, done func(error)) (string, bool, CacheState) {
	path, err := CachePath(c.directory, provider, id)
	if err != nil || !safeAssetURL(rawURL) {
		return "", false, CacheInvalid
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= c.maxBytes {
		return path, true, CacheHit
	}
	key := strings.ToLower(provider) + ":" + id
	c.mu.Lock()
	if failedAt, failed := c.failed[key]; failed && c.now().Sub(failedAt) < c.retryAfter {
		c.mu.Unlock()
		return "", false, CacheBackoff
	}
	if _, exists := c.pending[key]; exists {
		c.mu.Unlock()
		return "", false, CachePending
	}
	c.pending[key] = done
	c.mu.Unlock()
	go func() {
		downloadErr := c.download(path, rawURL)
		c.mu.Lock()
		callback := c.pending[key]
		delete(c.pending, key)
		if downloadErr != nil {
			c.failed[key] = c.now()
		} else {
			delete(c.failed, key)
		}
		c.mu.Unlock()
		if callback != nil {
			callback(downloadErr)
		}
	}()
	return "", false, CacheQueued
}

func (c *Cache) download(path, rawURL string) error {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("create emote download request")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("download emote image")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("emote image download failed (HTTP %d)", response.StatusCode)
	}
	if response.ContentLength > c.maxBytes {
		return errors.New("emote image exceeds download limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return errors.New("read emote image")
	}
	if int64(len(data)) > c.maxBytes {
		return errors.New("emote image exceeds download limit")
	}
	if len(data) == 0 || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return errors.New("emote asset is not an image")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("create provider emote cache")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".emote-*.tmp")
	if err != nil {
		return errors.New("create temporary emote cache file")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if syncErr := temporary.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("write emote cache file")
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return errors.New("store emote cache file")
	}
	return nil
}

func safeAssetURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}
