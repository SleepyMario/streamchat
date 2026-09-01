package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeProvider struct {
	mu      sync.Mutex
	creates []CreateRequest
	deletes []string
	pod     Pod
	block   chan struct{}
}

func (f *fakeProvider) Get(context.Context, string) (Pod, error) { return f.pod, nil }
func (f *fakeProvider) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	return nil
}

// captureProvider avoids clever mocking: it records the exact provider request
// so safety-sensitive defaults stay covered by a normal unit test.
type captureProvider struct{ fakeProvider }

func (f *captureProvider) Create(_ context.Context, request CreateRequest) (Pod, error) {
	f.mu.Lock()
	f.creates = append(f.creates, request)
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	return f.pod, nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{Enabled: true, APIBaseURL: "https://api.runpod.io/v2", APIKey: "not-a-real-key", Image: "example/worker:test", GPUTypeIDs: []string{"cheap-gpu", "fallback-gpu"}, CloudType: "SECURE", Model: "large-v3", AcceptedLanguages: "en,nl,de,zh,ko,vi,ja", WorkerPort: 8000, ContainerDiskGB: 30, ReadyTimeout: time.Hour, MaxRuntime: 6 * time.Hour, HeartbeatTimeout: time.Hour, MaxCostPerHour: 0.35, StatePath: t.TempDir() + "/session.json"}
}

func TestStartCreatesOneEphemeralWorkerAndStopDeletesIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &captureProvider{fakeProvider: fakeProvider{pod: Pod{ID: "pod-test", Cost: flexibleNumber(0.27), GPU: podGPU{DisplayName: "A4000"}}}}
	manager, err := New(ctx, testConfig(t), provider)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Token == "" || lease.PodID != "pod-test" || lease.WorkerURL != "https://pod-test-8000.proxy.runpod.net" {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	provider.mu.Lock()
	if len(provider.creates) != 1 {
		t.Fatalf("creates=%d", len(provider.creates))
	}
	request := provider.creates[0]
	provider.mu.Unlock()
	if request.Cloud != "SECURE" || request.Image != "example/worker:test" || request.DiskGB != 30 {
		t.Fatalf("unsafe create request: %#v", request)
	}
	if request.Env["SUBTITLE_AUTH_TOKEN"] != lease.Token || request.Env["SUBTITLE_MODEL"] != "large-v3" {
		t.Fatalf("worker environment does not match lease")
	}
	second, err := manager.Start(ctx)
	if err != nil || second.PodID != lease.PodID {
		t.Fatalf("idempotent start failed: %#v %v", second, err)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.creates) != 1 || len(provider.deletes) != 1 || provider.deletes[0] != "pod-test" {
		t.Fatalf("creates=%d deletes=%v", len(provider.creates), provider.deletes)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}
}

func TestRESTProviderUsesRunPodV2ContractAndOrderedFallback(t *testing.T) {
	var paths, gpuIDs []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing bearer authorization")
		}
		var body struct {
			Name  string `json:"name"`
			Image string `json:"image"`
			Cloud string `json:"cloud"`
			GPU   struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			} `json:"gpu"`
			Disk  int               `json:"disk"`
			Ports []string          `json:"ports"`
			Env   map[string]string `json:"env"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gpuIDs = append(gpuIDs, body.GPU.ID)
		if body.Name != "subtitles" || body.Image != "repo/worker:tag" || body.Cloud != "SECURE" || body.GPU.Count != 1 || body.Disk != 30 || len(body.Ports) != 1 || body.Ports[0] != "8000/http" || body.Env["TOKEN"] != "worker-secret" {
			t.Fatalf("unexpected v2 body: %#v", body)
		}
		if len(gpuIDs) == 1 {
			return jsonResponse(http.StatusUnprocessableEntity, `{"error":"no capacity"}`), nil
		}
		return jsonResponse(http.StatusCreated, `{"id":"pod-v2","status":"RUNNING","gpu":{"id":"fallback"},"cost":0.27}`), nil
	})}
	provider := RESTProvider{BaseURL: "https://api.runpod.io/v2", APIKey: "secret", Client: client}
	pod, err := provider.Create(context.Background(), CreateRequest{Name: "subtitles", Image: "repo/worker:tag", Cloud: "SECURE", GPUTypeIDs: []string{"cheap", "fallback"}, DiskGB: 30, Ports: []string{"8000/http"}, Env: map[string]string{"TOKEN": "worker-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if pod.ID != "pod-v2" || podCost(pod) != 0.27 || len(paths) != 2 || paths[0] != "/v2/pods" || paths[1] != "/v2/pods" || gpuIDs[0] != "cheap" || gpuIDs[1] != "fallback" {
		t.Fatalf("pod=%#v paths=%v gpuIDs=%v", pod, paths, gpuIDs)
	}
}

func TestRESTProviderDoesNotRetryAmbiguousFailure(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})}
	provider := RESTProvider{BaseURL: "https://api.runpod.io/v2", APIKey: "secret", Client: client}
	_, err := provider.Create(context.Background(), CreateRequest{Name: "subtitles", Image: "repo/worker:tag", GPUTypeIDs: []string{"cheap", "fallback"}})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestConcurrentCreateIsRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	block := make(chan struct{})
	provider := &captureProvider{fakeProvider: fakeProvider{pod: Pod{ID: "pod-test"}, block: block}}
	manager, err := New(ctx, testConfig(t), provider)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, startErr := manager.Start(ctx); done <- startErr }()
	deadline := time.Now().Add(time.Second)
	for {
		provider.mu.Lock()
		started := len(provider.creates) == 1
		provider.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first create did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.Start(ctx); err == nil {
		t.Fatal("concurrent create was not rejected")
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerAbovePriceCeilingIsDeleted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &captureProvider{fakeProvider: fakeProvider{pod: Pod{ID: "expensive", Cost: flexibleNumber(0.80)}}}
	manager, err := New(ctx, testConfig(t), provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx); err == nil {
		t.Fatal("worker above price ceiling was accepted")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.deletes) != 1 || provider.deletes[0] != "expensive" {
		t.Fatalf("expensive worker was not deleted: %v", provider.deletes)
	}
}
