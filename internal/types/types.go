package types

import "time"

// ContainerInfo Docker 컨테이너 정보
type ContainerInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Status       string            `json:"status"`
	State        string            `json:"state"`
	Ports        []PortMapping     `json:"ports"`
	Labels       map[string]string `json:"labels"`
	Networks     []NetworkInfo     `json:"networks"`
	Created      time.Time         `json:"created"`
	DockerHealth *DockerHealth     `json:"dockerHealth,omitempty"`
}

type PortMapping struct {
	Private  int    `json:"private"`
	Public   int    `json:"public,omitempty"`
	Protocol string `json:"protocol"`
	IP       string `json:"ip,omitempty"`
}

type NetworkInfo struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Gateway string `json:"gateway,omitempty"`
}

type DockerHealth struct {
	Status        string `json:"status"`
	FailingStreak int    `json:"failingStreak,omitempty"`
}

// ContainerType 컨테이너 타입 판별 결과
type ContainerType struct {
	Type       string `json:"type"`       // api, web, db, cache, worker, proxy, unknown
	Subtype    string `json:"subtype"`    // mysql, redis, nginx 등
	Confidence int    `json:"confidence"` // 0-100
	Source     string `json:"source"`     // label, image, port, default
}

// HealthStatus 헬스 상태
type HealthStatus string

const (
	StatusUp       HealthStatus = "UP"
	StatusDown     HealthStatus = "DOWN"
	StatusDegraded HealthStatus = "DEGRADED"
	StatusUnknown  HealthStatus = "UNKNOWN"
)

// HealthResult 헬스체크 결과
type HealthResult struct {
	Status       HealthStatus `json:"status"`
	Message      string       `json:"message,omitempty"`
	ResponseTime int64        `json:"responseTime,omitempty"` // ms
	CheckedAt    time.Time    `json:"checkedAt"`
}

// ContainerState 컨테이너 상태 (내부 저장용)
type ContainerState struct {
	Container ContainerInfo
	Type      ContainerType
	Health    HealthResult
}

// ==================== API 페이로드 ====================

// AgentInfo 에이전트 정보
type AgentInfo struct {
	AgentID   string    `json:"agentId"`
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
}

// ContainerReport 컨테이너 보고 데이터
type ContainerReport struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Image  string        `json:"image"`
	Type   ContainerType `json:"type"`
	Health HealthResult  `json:"health"`
	Ports  []PortMapping `json:"ports"`
}

// ReportPayload 상태 보고 페이로드
type ReportPayload struct {
	AgentID    string            `json:"agentId"`
	Hostname   string            `json:"hostname"`
	Timestamp  string            `json:"timestamp"`
	Containers []ContainerReport `json:"containers"`
	Stats      ReportStats       `json:"stats"`
}

type ReportStats struct {
	Total    int `json:"total"`
	Up       int `json:"up"`
	Down     int `json:"down"`
	Degraded int `json:"degraded"`
}

// AlertPayload 알림 페이로드
type AlertPayload struct {
	AgentID        string       `json:"agentId"`
	Hostname       string       `json:"hostname"`
	Timestamp      string       `json:"timestamp"`
	Container      AlertContainer `json:"container"`
	PreviousStatus HealthStatus `json:"previousStatus"`
	CurrentStatus  HealthStatus `json:"currentStatus"`
	Message        string       `json:"message,omitempty"`
}

type AlertContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	Type  string `json:"type"`
}

// APIResponse 서버 응답
type APIResponse struct {
	Status       int         `json:"status"`
	ResultMsg    string      `json:"resultMsg"`
	DivisionCode string      `json:"divisionCode"`
	Data         interface{} `json:"data"`
}
