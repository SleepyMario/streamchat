package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const subtitleFrameBytes = 3200

func runSubtitleSender(ctx context.Context, cfg subtitleConfig, workerURL, token string, onReady func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(workerURL, "/") + "/v1/live")
	if err != nil {
		return err
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else if endpoint.Scheme == "http" {
		endpoint.Scheme = "ws"
	}
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		return fmt.Errorf("connect to subtitle worker: %w", err)
	}
	defer connection.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopClose()
	_ = connection.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer clearSubtitleOutputs(cfg)

	if err := connection.WriteJSON(map[string]any{"language": "auto", "speech_threshold": 420}); err != nil {
		return err
	}
	var ready map[string]any
	if err := connection.ReadJSON(&ready); err != nil {
		return err
	}
	if ready["type"] != "ready" {
		return fmt.Errorf("unexpected subtitle worker response: %v", ready)
	}

	_ = connection.SetReadDeadline(time.Time{})
	source, err := resolveSubtitleSource(ctx, cfg.Source)
	if err != nil {
		return err
	}
	ffmpeg := exec.CommandContext(ctx, agentConfig.FFmpeg, "-hide_banner", "-loglevel", "warning", "-f", "dshow", "-i", "audio="+source, "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
	hideWindow(ffmpeg)
	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		return err
	}
	ffmpeg.Stderr = os.Stderr
	if err := ffmpeg.Start(); err != nil {
		return fmt.Errorf("start microphone capture: %w", err)
	}
	onReady()
	defer func() {
		if ffmpeg.Process != nil {
			_ = ffmpeg.Process.Kill()
		}
		_ = ffmpeg.Wait()
	}()

	errorsCh := make(chan error, 3)
	progress := make(chan struct{}, 1)
	go func() { errorsCh <- watchCapture(ctx, progress, 10*time.Second) }()
	var closeOnce sync.Once
	closeConnection := func() { closeOnce.Do(func() { _ = connection.Close() }) }
	go func() {
		reader := bufio.NewReaderSize(stdout, subtitleFrameBytes)
		buffer := make([]byte, subtitleFrameBytes)
		for {
			n, readErr := io.ReadFull(reader, buffer)
			if n > 0 {
				select {
				case progress <- struct{}{}:
				default:
				}
				_ = connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if writeErr := connection.WriteMessage(websocket.BinaryMessage, buffer[:n]); writeErr != nil {
					errorsCh <- writeErr
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					_ = connection.WriteMessage(websocket.TextMessage, []byte("flush"))
					errorsCh <- nil
				} else {
					errorsCh <- readErr
				}
				return
			}
		}
	}()
	go func() {
		for {
			_, payload, readErr := connection.ReadMessage()
			if readErr != nil {
				errorsCh <- readErr
				return
			}
			var event struct {
				Type              string `json:"type"`
				Original          string `json:"original"`
				English           string `json:"english"`
				SimplifiedChinese string `json:"simplified_chinese"`
			}
			if json.Unmarshal(payload, &event) != nil {
				continue
			}
			if event.Type == "flushed" {
				errorsCh <- nil
				return
			}
			if event.Type != "caption" {
				continue
			}
			if err := atomicText(cfg.OriginalOutput, strings.TrimSpace(event.Original)); err != nil {
				errorsCh <- err
				return
			}
			english := strings.TrimSpace(event.English)
			if english == strings.TrimSpace(event.Original) {
				english = ""
			}
			if err := atomicText(cfg.EnglishOutput, english); err != nil {
				errorsCh <- err
				return
			}
			chinese := strings.TrimSpace(event.SimplifiedChinese)
			if chinese == strings.TrimSpace(event.Original) || chinese == english {
				chinese = ""
			}
			if err := atomicText(cfg.ChineseOutput, chinese); err != nil {
				errorsCh <- err
				return
			}
			combined := combineCaption(event.Original, english, chinese)
			if err := atomicText(cfg.CombinedOutput, combined); err != nil {
				errorsCh <- err
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		closeConnection()
		return ctx.Err()
	case runErr := <-errorsCh:
		closeConnection()
		return runErr
	}
}

func resolveSubtitleSource(ctx context.Context, configured string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, agentConfig.FFmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	hideWindow(cmd)
	b, _ := cmd.CombinedOutput()
	for _, name := range strings.Split(configured, "|") {
		name = strings.TrimSpace(name)
		if name != "" && strings.Contains(string(b), "\""+name+"\" (audio)") {
			return name, nil
		}
	}
	return "", fmt.Errorf("configured wireless microphone is not connected")
}

func combineCaption(original, english, chinese string) string {
	lines := make([]string, 0, 3)
	for _, line := range []string{original, english, chinese} {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		duplicate := false
		for _, existing := range lines {
			if line == existing {
				duplicate = true
				break
			}
		}
		if !duplicate {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func atomicText(path, value string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".subtitle-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0600); err == nil {
		// OBS's FreeType text source can retain its previous contents when a
		// watched file is replaced by a zero-byte file. A newline is visually
		// empty but reliably tells OBS to clear the stale subtitle row.
		_, err = temporary.WriteString(value + "\n")
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func clearSubtitleOutputs(cfg subtitleConfig) {
	_ = atomicText(cfg.OriginalOutput, "")
	_ = atomicText(cfg.EnglishOutput, "")
	_ = atomicText(cfg.ChineseOutput, "")
	_ = atomicText(cfg.CombinedOutput, "")
}

// Silence still produces PCM frames. Only absent frames indicate a stalled device.
func watchCapture(ctx context.Context, progress <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-progress:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			return errors.New("microphone capture stopped delivering audio")
		}
	}
}

func retrySubtitleSender(ctx context.Context, delay time.Duration, send func() error, state func(string, string)) error {
	for {
		_ = send()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// EOF is also unexpected until the user stops the live session.
		state("reconnecting", "Subtitle connection interrupted; reconnecting microphone")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
