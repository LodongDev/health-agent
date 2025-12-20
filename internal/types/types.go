package types

import "time"

// 상태 타입
type Status string

const (
	StatusUp      Status = "UP"
	StatusDown    Status = "DOWN"
	StatusWarn    Status = "WARN"
	StatusUnknown Status = "UNKNOWN"
)

// 서비스 타입
type ServiceType string

const (
	// Database
	TypeMySQL      ServiceType = "MYSQL"
	TypePostgreSQL ServiceType = "POSTGRESQL"
	TypeRedis      ServiceType = "REDIS"
	TypeMongoDB    ServiceType = "MONGODB"

	// API - 언어별 구분
	TypeSpring     ServiceType = "API_JAVA"     // Spring Boot (Java)
	TypeAPIJava    ServiceType = "API_JAVA"     // Java API
	TypeAPIPython  ServiceType = "API_PYTHON"   // Python API (FastAPI, Flask, Django)
	TypeAPINode    ServiceType = "API_NODE"     // Node.js API
	TypeAPIGo      ServiceType = "API_GO"       // Go API
	TypeAPI        ServiceType = "API"          // 일반 API

	// Web
	TypeWebNginx   ServiceType = "WEB_NGINX"    // Nginx
	TypeWebApache  ServiceType = "WEB_APACHE"   // Apache HTTPD
	TypeWeb        ServiceType = "WEB"          // 일반 Web

	// Container
	TypeDocker     ServiceType = "CONTAINER"
	TypeUnknown    ServiceType = "UNKNOWN"
)

// ServiceState 서비스 상태
type ServiceState struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Type         ServiceType `json:"type"`
	Status       Status      `json:"status"`
	Message      string      `json:"message"`
	ResponseTime int         `json:"responseTime"` // ms
	CheckedAt    time.Time   `json:"checkedAt"`

	// 추가 정보
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Path       string `json:"path,omitempty"`       // 설정 파일 또는 실행 파일 경로
	ConfigPath string `json:"configPath,omitempty"` // 설정 파일 경로
}

// AgentReport 에이전트 보고서
type AgentReport struct {
	AgentID   string         `json:"agentId"`
	Hostname  string         `json:"hostname"`
	IP        string         `json:"ip"`
	Timestamp time.Time      `json:"timestamp"`
	Services  []ServiceState `json:"services"`
}

// WebSocketMessage 웹소켓 메시지
type WebSocketMessage struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	Timestamp int64       `json:"timestamp"`
}

// ContainerInfo Docker 컨테이너 정보
type ContainerInfo struct {
	ID          string
	Name        string
	Image       string
	Status      string
	State       string
	Labels      map[string]string
	Ports       []PortMapping
	Networks    []NetworkInfo
	Created     time.Time
	HealthCheck *DockerHealth
}

// PortMapping 포트 매핑
type PortMapping struct {
	Private  int
	Public   int
	Protocol string
	IP       string
}

// NetworkInfo 네트워크 정보
type NetworkInfo struct {
	Name    string
	IP      string
	Gateway string
}

// DockerHealth Docker 헬스체크 정보
type DockerHealth struct {
	Status        string
	FailingStreak int
}
