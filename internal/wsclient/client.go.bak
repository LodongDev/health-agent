package wsclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"health-agent/internal/types"

	"github.com/gorilla/websocket"
)

type Client struct {
	conn   *websocket.Conn
	url    string
	token  string
	mu     sync.Mutex
	closed bool
}

func New(url, token string) (*Client, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, header)
	if err != nil {
		return nil, fmt.Errorf("WebSocket 연결 실패: %w", err)
	}

	client := &Client{
		conn:  conn,
		url:   url,
		token: token,
	}

	// 연결 유지를 위한 ping
	go client.keepAlive()

	return client, nil
}

func (c *Client) keepAlive() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.conn.WriteMessage(websocket.PingMessage, nil)
		c.mu.Unlock()
	}
}

func (c *Client) SendReport(report types.AgentReport) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("연결이 닫혔습니다")
	}

	msg := types.WebSocketMessage{
		Type:      "AGENT_REPORT",
		Data:      report,
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("메시지 전송 실패: %w", err)
	}

	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	return c.conn.Close()
}
