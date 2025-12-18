package config

import (
	"fmt"
	"net"
	"os"

	"github.com/google/uuid"
)

// 고정 서버 주소
const (
	AuthURL          = "http://172.27.50.118:10709"
	MonitoringAPIURL = "http://172.27.50.181:8080"
	WebSocketURL     = "ws://172.27.50.181:8080/ws/monitoring"
)

// LoadOrCreateAgentID 에이전트 ID 로드 또는 생성
func LoadOrCreateAgentID() string {
	idFile := "/etc/health-agent/agent-id"
	if data, err := os.ReadFile(idFile); err == nil {
		return string(data)
	}

	id := fmt.Sprintf("agent-%s", uuid.New().String()[:8])

	os.MkdirAll("/etc/health-agent", 0755)
	os.WriteFile(idFile, []byte(id), 0644)

	return id
}

// GetLocalIP 로컬 IP 조회
func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
