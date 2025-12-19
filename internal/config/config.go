package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/uuid"
)

// 고정 서버 주소
const (
	AuthURL          = "http://172.27.50.118:10709"
	MonitoringAPIURL = "http://172.27.50.181:8080"
	WebSocketURL     = "ws://172.27.50.181:8080/ws/monitoring"
)

// AgentConfig 에이전트 설정
type AgentConfig struct {
	APIKey string `json:"apiKey"`
	Name   string `json:"name,omitempty"`
}

// getConfigDir 설정 디렉토리 경로
func getConfigDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("USERPROFILE"), ".health-agent")
	}
	return "/etc/health-agent"
}

// getConfigPath 설정 파일 경로
func getConfigPath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

// SaveConfig 설정 저장
func SaveConfig(cfg *AgentConfig) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("디렉토리 생성 실패: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	if err := os.WriteFile(getConfigPath(), data, 0600); err != nil {
		return fmt.Errorf("파일 저장 실패: %w", err)
	}

	return nil
}

// LoadConfig 설정 로드
func LoadConfig() (*AgentConfig, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("API 키가 설정되지 않았습니다. 'health-agent config --api-key <key>' 실행")
		}
		return nil, fmt.Errorf("설정 파일 읽기 실패: %w", err)
	}

	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("설정 파싱 실패: %w", err)
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API 키가 설정되지 않았습니다")
	}

	return &cfg, nil
}

// GetAPIKey API 키 조회
func GetAPIKey() (string, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return "", err
	}
	return cfg.APIKey, nil
}

// ConfigExists 설정 파일 존재 여부
func ConfigExists() bool {
	_, err := os.Stat(getConfigPath())
	return err == nil
}

// LoadOrCreateAgentID 에이전트 ID 로드 또는 생성
func LoadOrCreateAgentID() string {
	idFile := filepath.Join(getConfigDir(), "agent-id")
	if data, err := os.ReadFile(idFile); err == nil {
		return string(data)
	}

	id := fmt.Sprintf("agent-%s", uuid.New().String()[:8])

	os.MkdirAll(getConfigDir(), 0755)
	os.WriteFile(idFile, []byte(id), 0644)

	return id
}

// GetLocalIP 로컬 IP 조회 (기본 게이트웨이로 나가는 IP)
func GetLocalIP() string {
	// 방법 1: 외부로 연결 시도하여 사용되는 IP 확인
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		return localAddr.IP.String()
	}

	// 방법 2: 인터페이스 순회 (docker, veth 제외)
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range interfaces {
		// docker, veth, br- 등 가상 인터페이스 제외
		name := iface.Name
		if name == "lo" || name == "docker0" ||
			len(name) > 2 && name[:3] == "br-" ||
			len(name) > 3 && name[:4] == "veth" ||
			len(name) > 5 && name[:6] == "virbr" {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String()
				}
			}
		}
	}

	return "127.0.0.1"
}
