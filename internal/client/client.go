package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"docker-health-agent/internal/types"
)

// Client 중앙 서버 API 클라이언트
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New Client 생성
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// RegisterAgent 에이전트 등록
func (c *Client) RegisterAgent(ctx context.Context, agent types.AgentInfo) error {
	return c.post(ctx, "/agents/register", agent)
}

// Heartbeat 하트비트
func (c *Client) Heartbeat(ctx context.Context, agentID string) error {
	payload := map[string]string{
		"agentId":   agentID,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	return c.post(ctx, "/agents/heartbeat", payload)
}

// ReportContainers 컨테이너 상태 보고
func (c *Client) ReportContainers(ctx context.Context, payload types.ReportPayload) error {
	return c.post(ctx, "/containers/report", payload)
}

// SendAlert 알림 전송
func (c *Client) SendAlert(ctx context.Context, payload types.AlertPayload) error {
	return c.post(ctx, "/alerts", payload)
}

// Ping 연결 테스트
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return err
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("서버 응답: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) post(ctx context.Context, path string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("서버 응답: %d", resp.StatusCode)
	}

	return nil
}
