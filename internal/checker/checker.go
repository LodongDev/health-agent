package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"docker-health-agent/internal/types"
)

// Checker 헬스체크 수행
type Checker struct {
	timeout     time.Duration
	labelPrefix string
	httpClient  *http.Client
}

// New Checker 생성
func New(timeout time.Duration, labelPrefix string) *Checker {
	return &Checker{
		timeout:     timeout,
		labelPrefix: labelPrefix,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// Check 헬스체크 수행
func (c *Checker) Check(ctx context.Context, container types.ContainerInfo, ctype types.ContainerType) types.HealthResult {
	now := time.Now()

	// 컨테이너 상태 먼저 확인
	if container.State != "running" {
		return types.HealthResult{
			Status:    types.StatusDown,
			Message:   fmt.Sprintf("상태: %s", container.State),
			CheckedAt: now,
		}
	}

	// Docker HEALTHCHECK 실패 시
	if container.DockerHealth != nil && container.DockerHealth.Status == "unhealthy" {
		return types.HealthResult{
			Status:    types.StatusDown,
			Message:   "Docker HEALTHCHECK 실패",
			CheckedAt: now,
		}
	}

	// 타입별 체크
	switch ctype.Type {
	case "api":
		return c.checkAPI(ctx, container)
	case "web":
		return c.checkWeb(ctx, container)
	case "db":
		return c.checkDB(ctx, container, ctype.Subtype)
	case "cache":
		return c.checkCache(ctx, container, ctype.Subtype)
	case "proxy":
		return c.checkProxy(ctx, container)
	case "worker":
		return c.checkWorker(container)
	default:
		return c.checkContainerOnly(container)
	}
}

// ==================== API ====================

func (c *Checker) checkAPI(ctx context.Context, container types.ContainerInfo) types.HealthResult {
	url := c.getHTTPURL(container)
	if url == "" {
		return types.HealthResult{Status: types.StatusUnknown, Message: "HTTP 포트 없음", CheckedAt: time.Now()}
	}

	// 커스텀 헬스 엔드포인트
	customPath := container.Labels[c.labelPrefix+".health"]
	paths := []string{"/health", "/actuator/health", "/api/health", "/status"}
	if customPath != "" {
		paths = append([]string{customPath}, paths...)
	}

	for _, path := range paths {
		result := c.httpGet(ctx, url+path)
		if result.Status == types.StatusUp || result.Status == types.StatusDegraded {
			return result
		}
	}

	// Root 체크
	return c.httpGet(ctx, url)
}

// ==================== Web ====================

func (c *Checker) checkWeb(ctx context.Context, container types.ContainerInfo) types.HealthResult {
	url := c.getHTTPURL(container)
	if url == "" {
		return types.HealthResult{Status: types.StatusUnknown, Message: "HTTP 포트 없음", CheckedAt: time.Now()}
	}
	return c.httpGet(ctx, url)
}

// ==================== DB ====================

func (c *Checker) checkDB(ctx context.Context, container types.ContainerInfo, subtype string) types.HealthResult {
	portMap := map[string]int{
		"mysql": 3306, "postgres": 5432, "mongodb": 27017, "mssql": 1433, "oracle": 1521,
	}

	targetPort := portMap[subtype]
	if targetPort == 0 {
		targetPort = c.findDBPort(container)
	}

	if targetPort == 0 {
		return types.HealthResult{Status: types.StatusUnknown, Message: "DB 포트 없음", CheckedAt: time.Now()}
	}

	host, port := c.getHostPort(container, targetPort)
	start := time.Now()
	connected := c.tcpConnect(ctx, host, port)

	return types.HealthResult{
		Status:       boolToStatus(connected),
		Message:      fmt.Sprintf("%s %s", subtype, boolToMsg(connected, "연결", "실패")),
		ResponseTime: time.Since(start).Milliseconds(),
		CheckedAt:    time.Now(),
	}
}

// ==================== Cache ====================

func (c *Checker) checkCache(ctx context.Context, container types.ContainerInfo, subtype string) types.HealthResult {
	if subtype == "redis" || c.hasPort(container, 6379) {
		return c.checkRedis(ctx, container)
	}
	if subtype == "memcached" || c.hasPort(container, 11211) {
		return c.checkMemcached(ctx, container)
	}
	return types.HealthResult{Status: types.StatusUnknown, Message: "Cache 타입 불명", CheckedAt: time.Now()}
}

func (c *Checker) checkRedis(ctx context.Context, container types.ContainerInfo) types.HealthResult {
	host, port := c.getHostPort(container, 6379)
	start := time.Now()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), c.timeout)
	if err != nil {
		return types.HealthResult{Status: types.StatusDown, Message: err.Error(), CheckedAt: time.Now()}
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(c.timeout))
	conn.Write([]byte("PING\r\n"))

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return types.HealthResult{Status: types.StatusDown, Message: "응답 없음", CheckedAt: time.Now()}
	}

	response := strings.TrimSpace(string(buf[:n]))
	isPong := response == "+PONG" || strings.Contains(response, "PONG")

	return types.HealthResult{
		Status:       boolToStatus(isPong),
		Message:      boolToMsg(isPong, "Redis PONG", response),
		ResponseTime: time.Since(start).Milliseconds(),
		CheckedAt:    time.Now(),
	}
}

func (c *Checker) checkMemcached(ctx context.Context, container types.ContainerInfo) types.HealthResult {
	host, port := c.getHostPort(container, 11211)
	start := time.Now()
	connected := c.tcpConnect(ctx, host, port)

	return types.HealthResult{
		Status:       boolToStatus(connected),
		Message:      boolToMsg(connected, "Memcached 연결", "연결 실패"),
		ResponseTime: time.Since(start).Milliseconds(),
		CheckedAt:    time.Now(),
	}
}

// ==================== Proxy ====================

func (c *Checker) checkProxy(ctx context.Context, container types.ContainerInfo) types.HealthResult {
	url := c.getHTTPURL(container)
	if url == "" {
		return types.HealthResult{Status: types.StatusUnknown, Message: "HTTP 포트 없음", CheckedAt: time.Now()}
	}
	return c.httpGet(ctx, url)
}

// ==================== Worker ====================

func (c *Checker) checkWorker(container types.ContainerInfo) types.HealthResult {
	if container.DockerHealth != nil {
		switch container.DockerHealth.Status {
		case "healthy":
			return types.HealthResult{Status: types.StatusUp, Message: "HEALTHCHECK 통과", CheckedAt: time.Now()}
		case "unhealthy":
			return types.HealthResult{Status: types.StatusDown, Message: "HEALTHCHECK 실패", CheckedAt: time.Now()}
		case "starting":
			return types.HealthResult{Status: types.StatusDegraded, Message: "시작 중", CheckedAt: time.Now()}
		}
	}

	return types.HealthResult{
		Status:    boolToStatus(container.State == "running"),
		Message:   fmt.Sprintf("실행 중 (HEALTHCHECK 없음)"),
		CheckedAt: time.Now(),
	}
}

// ==================== Unknown ====================

func (c *Checker) checkContainerOnly(container types.ContainerInfo) types.HealthResult {
	return types.HealthResult{
		Status:    boolToStatus(container.State == "running"),
		Message:   fmt.Sprintf("컨테이너 %s", container.State),
		CheckedAt: time.Now(),
	}
}

// ==================== 유틸리티 ====================

func (c *Checker) httpGet(ctx context.Context, url string) types.HealthResult {
	start := time.Now()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return types.HealthResult{
			Status:    types.StatusDown,
			Message:   err.Error(),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	responseTime := time.Since(start).Milliseconds()

	// 응답 코드 확인
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		// JSON 파싱 시도
		body, _ := io.ReadAll(resp.Body)
		status := c.parseHealthJSON(body)
		return types.HealthResult{
			Status:       status,
			Message:      fmt.Sprintf("HTTP %d", resp.StatusCode),
			ResponseTime: responseTime,
			CheckedAt:    time.Now(),
		}
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return types.HealthResult{
			Status:       types.StatusDegraded,
			Message:      fmt.Sprintf("HTTP %d", resp.StatusCode),
			ResponseTime: responseTime,
			CheckedAt:    time.Now(),
		}
	}

	return types.HealthResult{
		Status:       types.StatusDown,
		Message:      fmt.Sprintf("HTTP %d", resp.StatusCode),
		ResponseTime: responseTime,
		CheckedAt:    time.Now(),
	}
}

func (c *Checker) parseHealthJSON(body []byte) types.HealthStatus {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return types.StatusUp
	}

	// { status: "UP" }
	if status, ok := data["status"].(string); ok {
		switch strings.ToUpper(status) {
		case "UP", "HEALTHY", "OK":
			return types.StatusUp
		case "DOWN", "UNHEALTHY":
			return types.StatusDown
		case "DEGRADED", "PARTIAL":
			return types.StatusDegraded
		}
	}

	// { healthy: true }
	if healthy, ok := data["healthy"].(bool); ok {
		if healthy {
			return types.StatusUp
		}
		return types.StatusDown
	}

	return types.StatusUp
}

func (c *Checker) tcpConnect(ctx context.Context, host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), c.timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Checker) getHTTPURL(container types.ContainerInfo) string {
	// Label URL 우선
	if url := container.Labels[c.labelPrefix+".url"]; url != "" {
		return url
	}

	httpPorts := []int{80, 443, 8080, 8000, 3000, 5000}

	// Public 포트
	for _, p := range container.Ports {
		if p.Public > 0 {
			for _, hp := range httpPorts {
				if p.Private == hp {
					host := "localhost"
					if p.IP != "" && p.IP != "0.0.0.0" {
						host = p.IP
					}
					proto := "http"
					if p.Private == 443 {
						proto = "https"
					}
					return fmt.Sprintf("%s://%s:%d", proto, host, p.Public)
				}
			}
		}
	}

	// 네트워크 IP
	if len(container.Networks) > 0 {
		ip := container.Networks[0].IP
		for _, hp := range httpPorts {
			for _, p := range container.Ports {
				if p.Private == hp {
					return fmt.Sprintf("http://%s:%d", ip, hp)
				}
			}
		}
	}

	return ""
}

func (c *Checker) getHostPort(container types.ContainerInfo, targetPort int) (string, int) {
	// Public 포트
	for _, p := range container.Ports {
		if p.Private == targetPort && p.Public > 0 {
			host := "localhost"
			if p.IP != "" && p.IP != "0.0.0.0" {
				host = p.IP
			}
			return host, p.Public
		}
	}

	// 네트워크 IP
	if len(container.Networks) > 0 {
		return container.Networks[0].IP, targetPort
	}

	return "localhost", targetPort
}

func (c *Checker) hasPort(container types.ContainerInfo, port int) bool {
	for _, p := range container.Ports {
		if p.Private == port {
			return true
		}
	}
	return false
}

func (c *Checker) findDBPort(container types.ContainerInfo) int {
	dbPorts := []int{3306, 5432, 27017, 1433, 1521}
	for _, port := range dbPorts {
		if c.hasPort(container, port) {
			return port
		}
	}
	return 0
}

func boolToStatus(ok bool) types.HealthStatus {
	if ok {
		return types.StatusUp
	}
	return types.StatusDown
}

func boolToMsg(ok bool, success, fail string) string {
	if ok {
		return success
	}
	return fail
}
