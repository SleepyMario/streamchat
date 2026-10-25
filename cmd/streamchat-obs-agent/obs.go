package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type obsClient struct {
	url        string
	configPath string
	timeout    time.Duration
}

type obsMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type obsHello struct {
	RPCVersion     int `json:"rpcVersion"`
	Authentication *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication,omitempty"`
}

type obsResponse struct {
	RequestType   string `json:"requestType"`
	RequestID     string `json:"requestId"`
	RequestStatus struct {
		Result  bool   `json:"result"`
		Code    int    `json:"code"`
		Comment string `json:"comment,omitempty"`
	} `json:"requestStatus"`
	ResponseData json.RawMessage `json:"responseData,omitempty"`
}

func (c *obsClient) scenes(ctx context.Context) ([]string, string, error) {
	response, err := c.request(ctx, "GetSceneList", nil)
	if err != nil {
		return nil, "", err
	}
	var data struct {
		CurrentProgramSceneName string `json:"currentProgramSceneName"`
		Scenes                  []struct {
			Name string `json:"sceneName"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(response, &data); err != nil {
		return nil, "", fmt.Errorf("decode scene list: %w", err)
	}
	scenes := make([]string, 0, len(data.Scenes))
	for i := len(data.Scenes) - 1; i >= 0; i-- {
		scenes = append(scenes, data.Scenes[i].Name)
	}
	return scenes, data.CurrentProgramSceneName, nil
}

func (c *obsClient) switchScene(ctx context.Context, scene string) error {
	_, err := c.request(ctx, "SetCurrentProgramScene", map[string]string{"sceneName": scene})
	return err
}

func (c *obsClient) request(parent context.Context, requestType string, data any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: c.timeout}
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to OBS: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(1024 * 1024)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(c.timeout))
	_ = conn.SetWriteDeadline(time.Now().Add(c.timeout))

	var message obsMessage
	if err := conn.ReadJSON(&message); err != nil {
		return nil, fmt.Errorf("read OBS hello: %w", err)
	}
	if message.Op != 0 {
		return nil, fmt.Errorf("unexpected OBS hello opcode %d", message.Op)
	}
	var hello obsHello
	if err := json.Unmarshal(message.D, &hello); err != nil {
		return nil, fmt.Errorf("decode OBS hello: %w", err)
	}
	identify := map[string]any{"rpcVersion": 1, "eventSubscriptions": 0}
	if hello.Authentication == nil {
		return nil, fmt.Errorf("OBS authentication must be enabled")
	}
	if hello.Authentication != nil {
		password, err := c.password()
		if err != nil {
			return nil, err
		}
		identify["authentication"] = obsAuthentication(password, hello.Authentication.Salt, hello.Authentication.Challenge)
	}
	if err := conn.WriteJSON(map[string]any{"op": 1, "d": identify}); err != nil {
		return nil, fmt.Errorf("identify to OBS: %w", err)
	}
	if err := conn.ReadJSON(&message); err != nil {
		return nil, fmt.Errorf("OBS authentication failed: %w", err)
	}

	if message.Op != 2 {
		return nil, fmt.Errorf("OBS did not accept authentication")
	}
	requestID := fmt.Sprintf("streamchat-%d", time.Now().UnixNano())
	request := map[string]any{"requestType": requestType, "requestId": requestID}
	if data != nil {
		request["requestData"] = data
	}
	if err := conn.WriteJSON(map[string]any{"op": 6, "d": request}); err != nil {
		return nil, fmt.Errorf("send OBS request: %w", err)
	}
	for {
		if err := conn.ReadJSON(&message); err != nil {
			return nil, fmt.Errorf("read OBS response: %w", err)
		}
		if message.Op != 7 {
			continue
		}
		var response obsResponse
		if err := json.Unmarshal(message.D, &response); err != nil {
			return nil, fmt.Errorf("decode OBS response: %w", err)
		}
		if response.RequestID != requestID {
			continue
		}
		if !response.RequestStatus.Result {
			return nil, fmt.Errorf("OBS rejected %s (%d): %s", requestType, response.RequestStatus.Code, response.RequestStatus.Comment)
		}
		return response.ResponseData, nil
	}
}

func (c *obsClient) password() (string, error) {
	b, err := os.ReadFile(c.configPath)
	if err != nil {
		return "", fmt.Errorf("read OBS password file: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return "", fmt.Errorf("OBS password is empty")
	}
	return strings.TrimSpace(string(b)), nil
}

func obsAuthentication(password, salt, challenge string) string {
	secretHash := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(secretHash[:])
	authHash := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(authHash[:])
}
