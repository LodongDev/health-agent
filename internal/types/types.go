package types

import "time"

// 상태 타입 (API에서 최종 판정, 에이전트는 참고용으로만 사용)
type Status string

const (
	StatusUp       Status = "UP"
	StatusDown     Status = "DOWN"
	StatusWarn     Status = "WARN"
	StatusClosed   Status = "CLOSED"  // 사용자가 수동 종료 (docker stop)
	StatusUnknown  Status = "UNKNOWN"
)

// CheckResult HTTP 체크 결과 (raw 데이터)
type CheckResult struct {
	Success      bool   `json:"success"`      // 연결 성공 여부
	StatusCode   int    `json:"statusCode"`   // HTTP 상태 코드 (0=연결실패)
	ResponseTime int    `json:"responseTime"` // 응답 시간 (ms)
	Error        string `json:"error,omitempty"` // 에러 메시지
}

// ContainerType 컨테이너 타입 정보
type ContainerType struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype,omitempty"`
	Confidence int    `json:"confidence"`
	Source     string `json:"source"`
}

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
	TypeWeb        ServiceType = "WEB"          // 일반 Web (React, Next.js 등)

	// Module (AI/ML, 배치 프로그램 등)
	TypeModule     ServiceType = "MODULE"       // Python AI/ML, 독립 모듈

	// Container
	TypeDocker     ServiceType = "CONTAINER"
	TypeUnknown    ServiceType = "UNKNOWN"
)

// ServiceState 서비스 상태 (에이전트 → API 전송용)
type ServiceState struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Type      ServiceType `json:"type"`
	CheckedAt time.Time   `json:"checkedAt"`

	// 컨테이너 상태 (running, exited, etc.)
	ContainerState string `json:"containerState,omitempty"`

	// HTTP 체크 결과 (raw 데이터 - API에서 상태 판정)
	HttpCheck *CheckResult `json:"httpCheck,omitempty"`

	// 추가 정보
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Path       string `json:"path,omitempty"`       // 이미지 또는 실행 파일 경로
	ConfigPath string `json:"configPath,omitempty"` // 설정 파일 경로

	// SSL 인증서 정보
	SSLError   bool   `json:"sslError,omitempty"`
	SSLMessage string `json:"sslMessage,omitempty"`

	// 웹 리소스 체크 결과 (raw 데이터)
	ResourceChecks []ResourceCheck `json:"resourceChecks,omitempty"`
}

// ResourceCheck 리소스 체크 결과 (raw 데이터)
type ResourceCheck struct {
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode"` // 0=연결실패, 200=정상, 4xx/5xx=에러
	Type       string `json:"type"`       // js, css, img 등
}

// ResourceError 리소스 에러 (브라우저 체크용)
type ResourceError struct {
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode"`
	Type       string `json:"type"`
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
