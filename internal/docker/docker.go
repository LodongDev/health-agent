package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"health-agent/internal/types"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type Checker struct {
	client  *client.Client
	timeout time.Duration
}

func New() *Checker {
	// OS에 따라 Docker 소켓 경로 결정
	var cli *client.Client
	var err error

	if runtime.GOOS == "windows" {
		// Windows: named pipe 사용
		cli, err = client.NewClientWithOpts(
			client.WithHost("npipe:////./pipe/docker_engine"),
			client.WithAPIVersionNegotiation(),
		)
	} else {
		// Linux/Mac: Unix socket 사용
		cli, err = client.NewClientWithOpts(
			client.WithHost("unix:///var/run/docker.sock"),
			client.WithAPIVersionNegotiation(),
		)
	}

	if err != nil {
		return &Checker{timeout: 5 * time.Second}
	}
	return &Checker{client: cli, timeout: 5 * time.Second}
}

func (c *Checker) Ping(ctx context.Context) error {
	if c.client == nil {
		return fmt.Errorf("Docker 클라이언트 초기화 실패")
	}
	_, err := c.client.Ping(ctx)
	return err
}

func (c *Checker) CheckAll(ctx context.Context) ([]types.ServiceState, error) {
	if c.client == nil {
		return nil, fmt.Errorf("Docker 클라이언트 없음")
	}

	containers, err := c.client.ContainerList(ctx, dockertypes.ContainerListOptions{All: false})
	if err != nil {
		return nil, err
	}

	var results []types.ServiceState
	for _, cont := range containers {
		state := c.checkContainer(ctx, cont)
		results = append(results, state)
	}
	return results, nil
}

func (c *Checker) checkContainer(ctx context.Context, cont dockertypes.Container) types.ServiceState {
	name := strings.TrimPrefix(cont.Names[0], "/")
	svcType := c.detectServiceType(cont)
	start := time.Now()

	state := types.ServiceState{
		ID:        fmt.Sprintf("docker-%s", cont.ID[:12]),
		Name:      name,
		Type:      svcType,
		CheckedAt: time.Now(),
	}

	// 컨테이너 상세 정보 가져오기
	inspect, err := c.client.ContainerInspect(ctx, cont.ID)
	if err == nil {
		// 이미지 이름을 경로로 사용
		state.Path = cont.Image

		// 컨테이너 IP 설정
		for _, network := range inspect.NetworkSettings.Networks {
			if network.IPAddress != "" {
				state.Host = network.IPAddress
				break
			}
		}

		// 포트 정보 설정
		for _, p := range cont.Ports {
			if p.PrivatePort > 0 {
				state.Port = int(p.PrivatePort)
				break
			}
		}

		// Docker HEALTHCHECK가 unhealthy면 바로 반환
		if inspect.State.Health != nil && inspect.State.Health.Status == "unhealthy" {
			state.Status = types.StatusDown
			state.Message = "Docker HEALTHCHECK: unhealthy"
			state.ResponseTime = int(time.Since(start).Milliseconds())
			return state
		}
		// healthy인 경우에도 실제 HTTP 체크를 수행하여 응답시간 측정
	}

	// 서비스 타입별 체크
	switch svcType {
	case types.TypeAPIJava:
		state = c.checkSpringApp(ctx, cont, state)
	case types.TypeWebNginx, types.TypeWebApache, types.TypeWeb:
		state = c.checkWebApp(ctx, cont, state)
	case types.TypeAPI, types.TypeAPIPython, types.TypeAPINode, types.TypeAPIGo:
		state = c.checkAPIApp(ctx, cont, state)
	case types.TypeMySQL, types.TypePostgreSQL, types.TypeRedis, types.TypeMongoDB:
		state = c.checkDBService(ctx, cont, state)
	default:
		// running 상태만 확인
		if cont.State == "running" {
			state.Status = types.StatusUp
			state.Message = "컨테이너 실행 중"
		} else {
			state.Status = types.StatusDown
			state.Message = fmt.Sprintf("상태: %s", cont.State)
		}
		state.ResponseTime = int(time.Since(start).Milliseconds())
	}
	return state
}

func (c *Checker) detectServiceType(cont dockertypes.Container) types.ServiceType {
	image := strings.ToLower(cont.Image)
	name := strings.ToLower(cont.Names[0])

	// 1. 라벨 기반 감지 (최우선)
	if svcType, ok := cont.Labels["health.type"]; ok {
		switch strings.ToLower(svcType) {
		case "spring", "java", "api_java":
			return types.TypeAPIJava
		case "python", "fastapi", "flask", "django", "api_python":
			return types.TypeAPIPython
		case "node", "nodejs", "api_node":
			return types.TypeAPINode
		case "go", "golang", "api_go":
			return types.TypeAPIGo
		case "api":
			return types.TypeAPI
		case "web", "nginx":
			return types.TypeWebNginx
		case "apache":
			return types.TypeWebApache
		case "mysql":
			return types.TypeMySQL
		case "postgresql", "postgres":
			return types.TypePostgreSQL
		case "redis":
			return types.TypeRedis
		case "mongodb", "mongo":
			return types.TypeMongoDB
		}
	}

	// 2. 이미지 기반 감지
	// Database
	if strings.Contains(image, "mysql") || strings.Contains(image, "mariadb") {
		return types.TypeMySQL
	}
	if strings.Contains(image, "postgres") {
		return types.TypePostgreSQL
	}
	if strings.Contains(image, "redis") {
		return types.TypeRedis
	}
	if strings.Contains(image, "mongo") {
		return types.TypeMongoDB
	}

	// Web servers
	if strings.Contains(image, "nginx") {
		return types.TypeWebNginx
	}
	if strings.Contains(image, "httpd") || strings.Contains(image, "apache") {
		return types.TypeWebApache
	}

	// API - 언어/프레임워크 감지
	if strings.Contains(image, "python") || strings.Contains(image, "fastapi") ||
		strings.Contains(image, "flask") || strings.Contains(image, "django") ||
		strings.Contains(name, "python") || strings.Contains(name, "fastapi") {
		return types.TypeAPIPython
	}
	if strings.Contains(image, "node") || strings.Contains(image, "npm") ||
		strings.Contains(name, "node") || strings.Contains(name, "express") {
		return types.TypeAPINode
	}
	if strings.Contains(image, "golang") || strings.Contains(name, "-go") ||
		strings.Contains(name, "go-") {
		return types.TypeAPIGo
	}
	if strings.Contains(image, "java") || strings.Contains(image, "spring") ||
		strings.Contains(image, "openjdk") || strings.Contains(image, "jdk") ||
		strings.Contains(image, "maven") || strings.Contains(image, "gradle") ||
		strings.Contains(name, "spring") || strings.Contains(name, "-api") {
		return types.TypeAPIJava
	}

	// 3. 포트 기반 감지
	for _, p := range cont.Ports {
		switch p.PrivatePort {
		case 8080, 8081, 8082, 8000, 8888:
			// API 서버 (구체적 타입은 위에서 결정 안되면 일반 API)
			return types.TypeAPI
		case 80, 443:
			return types.TypeWeb
		case 3306:
			return types.TypeMySQL
		case 5432:
			return types.TypePostgreSQL
		case 6379:
			return types.TypeRedis
		case 27017:
			return types.TypeMongoDB
		case 3000:
			return types.TypeAPINode // Node.js 기본 포트
		case 5000:
			return types.TypeAPIPython // Flask 기본 포트
		}
	}

	return types.TypeDocker
}

func (c *Checker) checkSpringApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	endpoints := []string{"/actuator/health", "/health", "/"}
	var lastElapsed int
	for _, ep := range endpoints {
		url := fmt.Sprintf("http://%s:%d%s", ip, port, ep)
		status, msg, elapsed := c.httpCheck(url)
		lastElapsed = elapsed
		if status == types.StatusUp {
			state.Status = status
			state.Message = fmt.Sprintf("%s -> %s", ep, msg)
			state.Endpoint = ep
			state.ResponseTime = elapsed
			return state
		}
	}
	state.Status = types.StatusDown
	state.Message = "모든 엔드포인트 체크 실패"
	state.ResponseTime = lastElapsed
	return state
}

func (c *Checker) checkWebApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	url := fmt.Sprintf("http://%s:%d/", ip, port)
	status, msg, elapsed := c.httpCheck(url)
	state.Status = status
	state.Message = msg
	state.Endpoint = "/"
	state.ResponseTime = elapsed
	return state
}

func (c *Checker) checkAPIApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	endpoints := []string{"/health", "/api/health", "/"}
	var lastElapsed int
	for _, ep := range endpoints {
		url := fmt.Sprintf("http://%s:%d%s", ip, port, ep)
		status, msg, elapsed := c.httpCheck(url)
		lastElapsed = elapsed
		if status == types.StatusUp {
			state.Status = status
			state.Message = fmt.Sprintf("%s -> %s", ep, msg)
			state.Endpoint = ep
			state.ResponseTime = elapsed
			return state
		}
	}
	state.Status = types.StatusDown
	state.Message = "모든 엔드포인트 체크 실패"
	state.ResponseTime = lastElapsed
	return state
}

func (c *Checker) checkDBService(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	var port int

	switch state.Type {
	case types.TypeMySQL:
		port = 3306
	case types.TypePostgreSQL:
		port = 5432
	case types.TypeRedis:
		port = 6379
	case types.TypeMongoDB:
		port = 27017
	default:
		port = 0
	}

	// 포트 연결 테스트 (응답 시간 측정)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("포트 %d 연결 실패", port)
		state.ResponseTime = elapsed
		return state
	}
	conn.Close()

	state.Status = types.StatusUp
	state.Message = fmt.Sprintf("포트 %d 연결 정상", port)
	state.Port = port
	state.Host = ip
	state.ResponseTime = elapsed
	return state
}

// httpCheck HTTP 요청을 통해 상태를 확인하고 응답 시간을 반환
func (c *Checker) httpCheck(url string) (types.Status, string, int) {
	client := &http.Client{Timeout: c.timeout}
	start := time.Now()
	resp, err := client.Get(url)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		return types.StatusDown, fmt.Sprintf("연결 실패: %v", err), elapsed
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return types.StatusUp, fmt.Sprintf("%d OK", resp.StatusCode), elapsed
	}
	if resp.StatusCode >= 500 {
		return types.StatusDown, fmt.Sprintf("%d %s", resp.StatusCode, string(body)), elapsed
	}
	return types.StatusWarn, fmt.Sprintf("%d %s", resp.StatusCode, resp.Status), elapsed
}

func (c *Checker) getContainerIP(ctx context.Context, containerID string) string {
	inspect, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "127.0.0.1"
	}
	for _, network := range inspect.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress
		}
	}
	return "127.0.0.1"
}

func (c *Checker) getHTTPPort(cont dockertypes.Container) int {
	// 우선순위: 8080, 80, 443, 첫 번째 포트
	priorities := []uint16{8080, 80, 443, 8081, 8082, 3000}
	for _, p := range priorities {
		for _, cp := range cont.Ports {
			if cp.PrivatePort == p {
				return int(p)
			}
		}
	}
	if len(cont.Ports) > 0 {
		return int(cont.Ports[0].PrivatePort)
	}
	return 8080
}
