package wsclient

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"health-agent/internal/types"

	"github.com/gorilla/websocket"
)

type Client struct {
	conn      *websocket.Conn
	url       string
	apiKey    string
	mu        sync.Mutex
	closed    bool
	connected bool
}

func New(url, apiKey string) (*Client, error) {
	client := &Client{
		url:    url,
		apiKey: apiKey,
	}

	if err := client.connect(); err != nil {
		return nil, err
	}

	go client.keepAlive()

	return client, nil
}

func (c *Client) connect() error {
	header := http.Header{}
	header.Set("X-API-Key", c.apiKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(c.url, header)
	if err != nil {
		return fmt.Errorf("WebSocket 연결 실패: %w", err)
	}

	c.conn = conn
	c.connected = true
	return nil
}

func (c *Client) reconnect() {
	c.mu.Lock()
	c.connected = false
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for !c.closed {
		log.Printf("[INFO] 서버 재연결 시도 중...")

		if err := c.connect(); err != nil {
			log.Printf("[WARN] 재연결 실패: %v (다음 시도: %v 후)", err, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		log.Printf("[INFO] 서버 재연결 성공")
		return
	}
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

		if !c.connected || c.conn == nil {
			c.mu.Unlock()
			continue
		}

		err := c.conn.WriteMessage(websocket.PingMessage, nil)
		c.mu.Unlock()

		if err != nil {
			log.Printf("[WARN] Ping 실패, 재연결 시도...")
			c.reconnect()
		}
	}
}

func (c *Client) SendReport(report types.AgentReport) error {
	c.mu.Lock()

	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("연결이 닫혔습니다")
	}

	// 연결이 끊어진 경우 재연결 시도
	if !c.connected || c.conn == nil {
		c.mu.Unlock()
		c.reconnect()
		c.mu.Lock()
		// 재연결 후에도 연결이 안 됐으면 에러
		if !c.connected || c.conn == nil {
			c.mu.Unlock()
			return fmt.Errorf("서버에 연결할 수 없습니다")
		}
	}

	msg := types.WebSocketMessage{
		Type:      "AGENT_REPORT",
		Data:      report,
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.connected = false
		c.mu.Unlock()
		// 동기적으로 재연결 시도
		c.reconnect()
		return fmt.Errorf("메시지 전송 실패: %w", err)
	}

	c.mu.Unlock()
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	c.connected = false
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
