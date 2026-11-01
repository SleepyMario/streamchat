package obscontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type bitrateState struct {
	Kbps      int   `json:"kbps"`
	Live      bool  `json:"live"`
	Timestamp int64 `json:"timestamp"`
}

func (c *obsClient) bitrateParameter(ctx context.Context, name string, target any) error {
	b, err := c.request(ctx, "GetProfileParameter", map[string]string{"parameterCategory": "StreamchatBitrate", "parameterName": name})
	if err != nil {
		return err
	}
	var parameter struct {
		Value string `json:"parameterValue"`
	}
	if json.Unmarshal(b, &parameter) != nil || json.Unmarshal([]byte(parameter.Value), target) != nil {
		return fmt.Errorf("OBS bitrate helper unavailable")
	}
	return nil
}
func (c *obsClient) bitrateStatus(ctx context.Context) (bitrateState, error) {
	var s bitrateState
	if err := c.bitrateParameter(ctx, "State", &s); err != nil {
		return s, err
	}
	age := time.Now().Unix() - s.Timestamp
	if age < -5 || age > 10 {
		return s, fmt.Errorf("OBS bitrate helper is not responding")
	}
	return s, nil
}
func (c *obsClient) setBitrate(ctx context.Context, kbps int) (bitrateState, error) {
	if kbps != 5000 && kbps != 6000 && kbps != 8000 {
		return bitrateState{}, fmt.Errorf("Choose 5000, 6000 or 8000 kbps")
	}
	if _, err := c.bitrateStatus(ctx); err != nil {
		return bitrateState{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return bitrateState{}, err
	}
	id := hex.EncodeToString(nonce[:])
	payload, _ := json.Marshal(map[string]any{"id": id, "kbps": kbps, "expires": time.Now().Unix() + 7})
	if _, err := c.request(ctx, "SetProfileParameter", map[string]string{"parameterCategory": "StreamchatBitrate", "parameterName": "Request", "parameterValue": string(payload)}); err != nil {
		return bitrateState{}, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return bitrateState{}, fmt.Errorf("Bitrate confirmation timed out; check the current bitrate before retrying")
		case <-ticker.C:
			var result struct {
				ID    string `json:"id"`
				Kbps  int    `json:"kbps"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if c.bitrateParameter(ctx, "Result", &result) != nil || result.ID != id {
				continue
			}
			if !result.OK {
				return bitrateState{}, fmt.Errorf("Bitrate unchanged: %s", result.Error)
			}
			s, err := c.bitrateStatus(ctx)
			if err != nil {
				return s, err
			}
			if s.Kbps != kbps || result.Kbps != kbps {
				return s, fmt.Errorf("Bitrate read-back mismatch; refresh before retrying")
			}
			return s, nil
		}
	}
}
