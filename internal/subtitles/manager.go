package subtitles

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled           bool
	APIBaseURL        string
	APIKey            string
	Image             string
	GPUTypeIDs        []string
	CloudType         string
	Model             string
	AcceptedLanguages string
	WorkerPort        int
	ContainerDiskGB   int
	ReadyTimeout      time.Duration
	MaxRuntime        time.Duration
	HeartbeatTimeout  time.Duration
	MaxCostPerHour    float64
	StatePath         string
}

type Status struct {
	State         string    `json:"state"`
	PodID         string    `json:"pod_id,omitempty"`
	WorkerURL     string    `json:"worker_url,omitempty"`
	GPU           string    `json:"gpu,omitempty"`
	CostPerHour   float64   `json:"cost_per_hour,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	ReadyAt       time.Time `json:"ready_at,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	Deadline      time.Time `json:"deadline,omitempty"`
	Message       string    `json:"message,omitempty"`
}

type Lease struct {
	Status
	Token string `json:"token"`
}

type CreateRequest struct {
	Name              string            `json:"name"`
	ImageName         string            `json:"imageName"`
	CloudType         string            `json:"cloudType"`
	ComputeType       string            `json:"computeType"`
	GPUCount          int               `json:"gpuCount"`
	GPUTypeIDs        []string          `json:"gpuTypeIds"`
	GPUTypePriority   string            `json:"gpuTypePriority"`
	ContainerDiskInGB int               `json:"containerDiskInGb"`
	VolumeInGB        int               `json:"volumeInGb"`
	Ports             []string          `json:"ports"`
	Env               map[string]string `json:"env"`
	Interruptible     bool              `json:"interruptible"`
}

type Pod struct {
	ID            string         `json:"id"`
	DesiredStatus string         `json:"desiredStatus"`
	GPU           podGPU         `json:"gpu"`
	Cost          flexibleNumber `json:"costPerHr"`
	AdjustedCost  flexibleNumber `json:"adjustedCostPerHr"`
}

type podGPU struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type flexibleNumber float64

func (n *flexibleNumber) UnmarshalJSON(value []byte) error {
	var number float64
	if len(value) > 0 && value[0] == '"' {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return err
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return err
		}
		number = parsed
	} else if err := json.Unmarshal(value, &number); err != nil {
		return err
	}
	*n = flexibleNumber(number)
	return nil
}

type Provider interface {
	Create(context.Context, CreateRequest) (Pod, error)
	Get(context.Context, string) (Pod, error)
	Delete(context.Context, string) error
}

type RESTProvider struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (p RESTProvider) request(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return fmt.Errorf("RunPod API %s %s: %s", method, path, strings.TrimSpace(string(message)))
	}
	if result != nil && response.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			return err
		}
	}
	return nil
}

func (p RESTProvider) Create(ctx context.Context, request CreateRequest) (Pod, error) {
	var pod Pod
	err := p.request(ctx, http.MethodPost, "/pods", request, &pod)
	return pod, err
}
func (p RESTProvider) Get(ctx context.Context, id string) (Pod, error) {
	var pod Pod
	err := p.request(ctx, http.MethodGet, "/pods/"+url.PathEscape(id), nil, &pod)
	return pod, err
}
func (p RESTProvider) Delete(ctx context.Context, id string) error {
	return p.request(ctx, http.MethodDelete, "/pods/"+url.PathEscape(id), nil, nil)
}

type persisted struct {
	Status
	Token string `json:"token"`
}

type Manager struct {
	cfg      Config
	provider Provider
	health   *http.Client
	mu       sync.Mutex
	stopMu   sync.Mutex
	current  *persisted
	cancel   context.CancelFunc
	creating bool
}

func New(ctx context.Context, cfg Config, provider Provider) (*Manager, error) {
	if cfg.Enabled && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("RunPod API key is empty")
	}
	if provider == nil {
		provider = RESTProvider{BaseURL: cfg.APIBaseURL, APIKey: cfg.APIKey}
	}
	m := &Manager{cfg: cfg, provider: provider, health: &http.Client{Timeout: 5 * time.Second}}
	if err := m.load(); err != nil {
		return nil, err
	}
	if m.current != nil {
		// A service restart must not leave an unowned paid worker behind.
		go func() { _ = m.Stop(context.Background()) }()
	}
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.Stop(stopCtx)
	}()
	return m, nil
}

func (m *Manager) Start(ctx context.Context) (Lease, error) {
	if !m.cfg.Enabled {
		return Lease{}, errors.New("GPU subtitles are disabled")
	}
	m.mu.Lock()
	if m.current != nil {
		if m.current.State == "stopping" || m.current.State == "error" {
			m.mu.Unlock()
			return Lease{}, errors.New("the previous subtitle worker is still being cleaned up")
		}
		lease := Lease{Status: m.current.Status, Token: m.current.Token}
		m.mu.Unlock()
		return lease, nil
	}
	if m.creating {
		m.mu.Unlock()
		return Lease{}, errors.New("a subtitle worker is already being created")
	}
	m.creating = true
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		m.creating = false
		m.mu.Unlock()
		return Lease{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.creating = false; m.mu.Unlock() }()

	pod, err := m.provider.Create(ctx, CreateRequest{
		Name: "streamchat-subtitles", ImageName: m.cfg.Image, CloudType: m.cfg.CloudType,
		ComputeType: "GPU", GPUCount: 1, GPUTypeIDs: append([]string(nil), m.cfg.GPUTypeIDs...),
		GPUTypePriority: "custom", ContainerDiskInGB: m.cfg.ContainerDiskGB, VolumeInGB: 0,
		Ports: []string{fmt.Sprintf("%d/http", m.cfg.WorkerPort)}, Interruptible: false,
		Env: map[string]string{"SUBTITLE_AUTH_TOKEN": token, "SUBTITLE_MODEL": m.cfg.Model, "SUBTITLE_ACCEPTED_LANGUAGES": m.cfg.AcceptedLanguages},
	})
	if err != nil {
		return Lease{}, fmt.Errorf("create subtitle worker: %w", err)
	}
	if pod.ID == "" {
		return Lease{}, errors.New("RunPod returned a worker without an ID")
	}
	if cost := podCost(pod); cost > 0 && cost > m.cfg.MaxCostPerHour {
		_ = m.provider.Delete(context.Background(), pod.ID)
		return Lease{}, fmt.Errorf("RunPod worker costs $%.3f/hour, above the $%.3f/hour safety ceiling", cost, m.cfg.MaxCostPerHour)
	}
	now := time.Now().UTC()
	workerURL := fmt.Sprintf("https://%s-%d.proxy.runpod.net", pod.ID, m.cfg.WorkerPort)
	state := &persisted{Status: Status{State: "starting", PodID: pod.ID, WorkerURL: workerURL, GPU: gpuName(pod), CostPerHour: podCost(pod), StartedAt: now, LastHeartbeat: now, Deadline: now.Add(m.cfg.MaxRuntime), Message: "GPU worker is starting"}, Token: token}
	m.mu.Lock()
	if m.current != nil {
		m.mu.Unlock()
		_ = m.provider.Delete(context.Background(), pod.ID)
		return Lease{}, errors.New("another subtitle session started concurrently")
	}
	m.current = state
	monitorCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		_ = m.Stop(context.Background())
		return Lease{}, err
	}
	go m.monitor(monitorCtx, pod.ID)
	return Lease{Status: state.Status, Token: token}, nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return Status{State: "idle", Message: "No subtitle worker is running"}
	}
	return m.current.Status
}

func (m *Manager) Lease() (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return Lease{}, errors.New("no subtitle worker is running")
	}
	return Lease{Status: m.current.Status, Token: m.current.Token}, nil
}

func (m *Manager) Heartbeat() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return Status{}, errors.New("no subtitle worker is running")
	}
	m.current.LastHeartbeat = time.Now().UTC()
	if err := m.saveLocked(); err != nil {
		return Status{}, err
	}
	return m.current.Status, nil
}

func (m *Manager) Stop(ctx context.Context) error {
	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return nil
	}
	id := m.current.PodID
	m.current.State = "stopping"
	_ = m.saveLocked()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := m.provider.Delete(ctx, id); err != nil {
		m.mu.Lock()
		if m.current != nil && m.current.PodID == id {
			m.current.State = "error"
			m.current.Message = "Worker deletion failed: " + err.Error()
			_ = m.saveLocked()
		}
		m.mu.Unlock()
		return fmt.Errorf("delete subtitle worker: %w", err)
	}
	m.mu.Lock()
	if m.current != nil && m.current.PodID == id {
		m.current = nil
		m.cancel = nil
		_ = os.Remove(m.cfg.StatePath)
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) monitor(ctx context.Context, id string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	readyDeadline := time.Now().Add(m.cfg.ReadyTimeout)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.current == nil || m.current.PodID != id {
				m.mu.Unlock()
				return
			}
			deadline, heartbeat, state := m.current.Deadline, m.current.LastHeartbeat, m.current.State
			m.mu.Unlock()
			now := time.Now()
			if now.After(deadline) || now.Sub(heartbeat) > m.cfg.HeartbeatTimeout || (state == "starting" && now.After(readyDeadline)) {
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_ = m.Stop(stopCtx)
				cancel()
				return
			}
			pod, err := m.provider.Get(ctx, id)
			if err == nil {
				m.updatePod(pod)
			}
			if state == "starting" && m.ready(id) {
				m.markReady(id)
			}
		}
	}
}

func (m *Manager) ready(id string) bool {
	m.mu.Lock()
	if m.current == nil || m.current.PodID != id {
		m.mu.Unlock()
		return false
	}
	healthURL := m.current.WorkerURL + "/health"
	m.mu.Unlock()
	response, err := m.health.Get(healthURL)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		Ready bool `json:"ready"`
	}
	return json.NewDecoder(response.Body).Decode(&health) == nil && health.Ready
}

func (m *Manager) updatePod(pod Pod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.PodID != pod.ID {
		return
	}
	if name := gpuName(pod); name != "" {
		m.current.GPU = name
	}
	if cost := podCost(pod); cost > 0 {
		m.current.CostPerHour = cost
		if cost > m.cfg.MaxCostPerHour {
			m.current.State = "error"
			m.current.Message = fmt.Sprintf("Worker price $%.3f/hour exceeds safety ceiling", cost)
			go func() { _ = m.Stop(context.Background()) }()
		}
	}
	_ = m.saveLocked()
}

func (m *Manager) markReady(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.PodID != id || m.current.State != "starting" {
		return
	}
	m.current.State = "ready"
	m.current.ReadyAt = time.Now().UTC()
	m.current.Message = "GPU subtitle worker is ready"
	_ = m.saveLocked()
}

func gpuName(p Pod) string {
	if p.GPU.DisplayName != "" {
		return p.GPU.DisplayName
	}
	return p.GPU.ID
}
func podCost(p Pod) float64 {
	if p.AdjustedCost > 0 {
		return float64(p.AdjustedCost)
	}
	return float64(p.Cost)
}

func (m *Manager) saveLocked() error {
	if m.cfg.StatePath == "" || m.current == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.cfg.StatePath), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.current, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.cfg.StatePath), ".subtitle-session-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, m.cfg.StatePath)
}

func (m *Manager) load() error {
	if m.cfg.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(m.cfg.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state persisted
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("read subtitle session state: %w", err)
	}
	if state.PodID != "" {
		m.current = &state
	}
	return nil
}
