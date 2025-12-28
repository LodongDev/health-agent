package docker

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"health-agent/internal/browser"
	"health-agent/internal/config"
	"health-agent/internal/types"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// getMachineID 서버 고유 ID 반환 (machine-id 앞 8자리)
func getMachineID() string {
	// Linux: /etc/machine-id 사용
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/etc/machine-id"); err == nil {
			machineID := strings.TrimSpace(string(data))
			if len(machineID) >= 8 {
				return machineID[:8]
			}
		}
	}
	// Windows 또는 machine-id 없는 경우: IP 기반
	return strings.ReplaceAll(config.GetLocalIP(), ".", "-")
}

type Checker struct {
	client           *client.Client
	timeout          time.Duration
	lastResults      []types.ServiceState // 마지막 성공 결과 캐시
	lastRunningNames map[string]bool      // 이전에 실행 중이었던 컨테이너 이름
	browserChecker   *browser.Checker     // 브라우저 기반 네트워크 체커
	resourceErrorCache map[string]*resourceErrorState // 리소스 에러 캐시 (안정화용)
}

// resourceErrorState 리소스 에러 상태 추적
type resourceErrorState struct {
	errors          []types.ResourceError // 마지막으로 감지된 에러들
	consecutiveOK   int                   // 연속 정상 횟수
	lastCheckedAt   time.Time
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

	// 브라우저 체커 초기화
	browserChk := browser.New()
	if browserChk.IsAvailable() {
		log.Printf("[INFO] Browser-based network checking enabled (Chrome: %s)", browserChk.GetChromePath())
	} else {
		log.Printf("[WARN] Chrome not found. Using HTML parsing fallback for web resource checking.")
		log.Printf("[INFO] To enable full network capture, install Chrome:\n%s", browserChk.GetInstallCommand())
	}

	if err != nil {
		return &Checker{timeout: 5 * time.Second, browserChecker: browserChk, resourceErrorCache: make(map[string]*resourceErrorState)}
	}
	return &Checker{client: cli, timeout: 5 * time.Second, browserChecker: browserChk, resourceErrorCache: make(map[string]*resourceErrorState)}
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

	// 최대 3번 재시도 - 모든 컨테이너 조회 (종료된 것 포함)
	var allContainers []dockertypes.Container
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		allContainers, err = c.client.ContainerList(ctx, dockertypes.ContainerListOptions{All: true})
		if err == nil {
			break
		}
		log.Printf("[WARN] Docker API 호출 실패 (시도 %d/3): %v", attempt, err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * time.Second) // 1초, 2초 대기
		}
	}

	if err != nil {
		// 3번 모두 실패 시 캐시된 결과 반환
		if len(c.lastResults) > 0 {
			log.Printf("[WARN] Docker API 실패, 캐시된 결과 사용 (%d개 서비스)", len(c.lastResults))
			return c.lastResults, nil
		}
		return nil, err
	}

	// 무시 목록 로드
	ignoreList := config.GetIgnoreList()

	var results []types.ServiceState
	currentRunningNames := make(map[string]bool)

	for _, cont := range allContainers {
		name := strings.TrimPrefix(cont.Names[0], "/")

		// 무시 목록에 있으면 건너뛰기
		if isInIgnoreList(name, ignoreList) {
			log.Printf("[INFO] Skipping ignored container: %s", name)
			continue
		}

		if cont.State == "running" {
			// 실행 중인 컨테이너 → 정상 체크
			state := c.checkContainer(ctx, cont)
			results = append(results, state)
			currentRunningNames[name] = true
		} else if cont.State == "exited" {
			// 종료된 컨테이너 → 이전에 실행 중이었으면 CLOSED
			if c.lastRunningNames != nil && c.lastRunningNames[name] {
				log.Printf("[INFO] Container stopped by user: %s (state: %s)", name, cont.State)
				state := c.createClosedState(name, cont)
				results = append(results, state)
			}
		}
	}

	// 현재 실행 중인 컨테이너 목록 업데이트
	c.lastRunningNames = currentRunningNames

	// 성공 시 결과 캐시
	c.lastResults = results

	return results, nil
}

// createClosedState 수동 종료된 컨테이너의 CLOSED 상태 생성
func (c *Checker) createClosedState(name string, cont dockertypes.Container) types.ServiceState {
	return types.ServiceState{
		ID:           name, // 컨테이너 이름 = 서비스 ID (serverIp + name으로 고유성 보장)
		Name:         name,
		Type:         types.TypeDocker,
		Status:       types.StatusClosed,
		Message:      "컨테이너 수동 종료 (docker stop)",
		ResponseTime: 0,
		CheckedAt:    time.Now(),
		Path:         cont.Image,
	}
}

// isInIgnoreList 컨테이너 이름이 무시 목록에 있는지 확인
// 패턴 지원:
//   - "nginx-dev"  : 정확히 일치
//   - "dev-*"      : dev-로 시작하는 모든 컨테이너
//   - "*-dev"      : -dev로 끝나는 모든 컨테이너
//   - "*test*"     : test를 포함하는 모든 컨테이너
func isInIgnoreList(name string, ignoreList []string) bool {
	for _, pattern := range ignoreList {
		if matchPattern(name, pattern) {
			return true
		}
	}
	return false
}

// matchPattern 와일드카드 패턴 매칭
func matchPattern(name, pattern string) bool {
	// 정확히 일치
	if pattern == name {
		return true
	}

	hasPrefix := strings.HasPrefix(pattern, "*")
	hasSuffix := strings.HasSuffix(pattern, "*")

	// *test* : 포함
	if hasPrefix && hasSuffix && len(pattern) > 2 {
		substr := pattern[1 : len(pattern)-1]
		return strings.Contains(name, substr)
	}

	// *-dev : 접미사 매칭
	if hasPrefix && !hasSuffix {
		suffix := pattern[1:]
		return strings.HasSuffix(name, suffix)
	}

	// dev-* : 접두사 매칭
	if !hasPrefix && hasSuffix {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(name, prefix)
	}

	return false
}

func (c *Checker) checkContainer(ctx context.Context, cont dockertypes.Container) types.ServiceState {
	name := strings.TrimPrefix(cont.Names[0], "/")
	svcType := c.detectServiceType(cont)
	start := time.Now()

	// 서비스 ID = 컨테이너 이름 (serverIp + name으로 고유성 보장)
	state := types.ServiceState{
		ID:        name,
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
	log.Printf("[DEBUG] Container %s: type=%s, image=%s", name, svcType, cont.Image)
	switch svcType {
	case types.TypeAPIJava:
		log.Printf("[DEBUG] %s -> checkSpringApp", name)
		state = c.checkSpringApp(ctx, cont, state)
	case types.TypeWebNginx, types.TypeWebApache, types.TypeWeb:
		log.Printf("[DEBUG] %s -> checkWebApp", name)
		state = c.checkWebApp(ctx, cont, state)
	case types.TypeAPI, types.TypeAPIPython, types.TypeAPINode, types.TypeAPIGo:
		log.Printf("[DEBUG] %s -> checkAPIApp", name)
		state = c.checkAPIApp(ctx, cont, state)
	case types.TypeMySQL, types.TypePostgreSQL, types.TypeRedis, types.TypeMongoDB:
		log.Printf("[DEBUG] %s -> checkDBService", name)
		state = c.checkDBService(ctx, cont, state)
	default:
		log.Printf("[DEBUG] %s -> default (no HTTP check)", name)
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
	log.Printf("[DEBUG] %s: status=%s, responseTime=%dms, msg=%s", name, state.Status, state.ResponseTime, state.Message)
	return state
}

func (c *Checker) detectServiceType(cont dockertypes.Container) types.ServiceType {
	image := strings.ToLower(cont.Image)
	name := strings.ToLower(cont.Names[0])

	// 1. 컨테이너 내부 파일 구조로 감지 (가장 정확)
	if fileType := c.detectTypeByFileStructure(cont.ID); fileType != types.TypeDocker {
		log.Printf("[DEBUG] %s: detected by file structure -> %s", name, fileType)
		return fileType
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

	// Web servers (이미지 기반)
	if strings.Contains(image, "nginx") {
		return types.TypeWebNginx
	}
	if strings.Contains(image, "httpd") || strings.Contains(image, "apache") {
		return types.TypeWebApache
	}

	// API - 이름에 -api가 포함되면 우선 API로 처리 (web-api 같은 경우 API 우선)
	if strings.Contains(name, "-api") || strings.Contains(name, "_api") ||
		strings.Contains(image, "-api") || strings.Contains(image, "_api") {
		// Java/Spring 관련 추가 체크
		if strings.Contains(image, "java") || strings.Contains(image, "spring") ||
			strings.Contains(image, "openjdk") || strings.Contains(image, "jdk") ||
			strings.Contains(name, "spring") {
			return types.TypeAPIJava
		}
		// Python 관련 추가 체크
		if strings.Contains(image, "python") || strings.Contains(name, "python") ||
			strings.Contains(name, "fastapi") || strings.Contains(name, "flask") {
			return types.TypeAPIPython
		}
		return types.TypeAPI
	}

	// API - 언어/프레임워크 감지
	if strings.Contains(image, "python") || strings.Contains(image, "fastapi") ||
		strings.Contains(image, "flask") || strings.Contains(image, "django") ||
		strings.Contains(name, "python") || strings.Contains(name, "fastapi") ||
		strings.Contains(name, "ocr") || strings.Contains(name, "-engine") ||
		strings.Contains(name, "_engine") {
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
	// Java/Spring 관련
	if strings.Contains(image, "java") || strings.Contains(image, "spring") ||
		strings.Contains(image, "openjdk") || strings.Contains(image, "jdk") ||
		strings.Contains(image, "maven") || strings.Contains(image, "gradle") ||
		strings.Contains(name, "spring") {
		return types.TypeAPIJava
	}

	// 컨테이너/이미지 이름에 -web 또는 _web 포함시 WEB으로 판정 (API 체크 이후)
	if strings.Contains(name, "-web") || strings.Contains(name, "_web") ||
		strings.Contains(image, "-web") || strings.Contains(image, "_web") {
		return types.TypeWeb
	}

	return types.TypeDocker
}

// detectTypeByFileStructure 컨테이너 내부 파일 구조를 확인하여 타입 판별 (최적화: 단일 명령)
func (c *Checker) detectTypeByFileStructure(containerID string) types.ServiceType {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 단일 명령으로 여러 파일 존재 여부를 한번에 확인
	files := c.checkFilesInContainer(ctx, containerID)
	if files == nil {
		return types.TypeDocker
	}

	// 1. Web Server 확인
	if files["nginx"] {
		log.Printf("[DEBUG] %s: found nginx", containerID[:12])
		return types.TypeWebNginx
	}
	if files["apache"] {
		log.Printf("[DEBUG] %s: found apache", containerID[:12])
		return types.TypeWebApache
	}

	// 2. Next.js
	if files["nextjs"] {
		log.Printf("[DEBUG] %s: found Next.js", containerID[:12])
		return types.TypeWeb
	}

	// 3. Vite
	if files["vite"] {
		log.Printf("[DEBUG] %s: found Vite", containerID[:12])
		return types.TypeWeb
	}

	// 4. React (build/dist)
	if files["react_build"] {
		log.Printf("[DEBUG] %s: found React build", containerID[:12])
		return types.TypeWeb
	}

	// 5. React/Vite src
	if files["react_src"] && files["package_json"] {
		log.Printf("[DEBUG] %s: found React/Vite src", containerID[:12])
		return types.TypeWeb
	}

	// 6. Java/Spring
	if files["java"] {
		log.Printf("[DEBUG] %s: found Java/Spring", containerID[:12])
		return types.TypeAPIJava
	}

	// 7. Go
	if files["golang"] {
		log.Printf("[DEBUG] %s: found Go", containerID[:12])
		return types.TypeAPIGo
	}

	// 8. Python
	if files["python"] || files["python_api"] || files["python_module"] || files["ocr_ai"] {
		// OCR/AI 관련 Python
		if files["ocr_ai"] {
			log.Printf("[DEBUG] %s: found OCR/AI Python -> API_PYTHON", containerID[:12])
			return types.TypeAPIPython
		}
		// FastAPI/Flask/Django 등 API
		if files["python_api"] {
			log.Printf("[DEBUG] %s: found Python API", containerID[:12])
			return types.TypeAPIPython
		}
		// 일반 Python 모듈
		if files["python_module"] {
			log.Printf("[DEBUG] %s: found Python MODULE", containerID[:12])
			return types.TypeModule
		}
		// requirements.txt만 있는 경우 (API로 가정)
		log.Printf("[DEBUG] %s: found Python (requirements.txt) -> API_PYTHON", containerID[:12])
		return types.TypeAPIPython
	}

	// 9. Node.js (package.json만 있는 경우)
	if files["package_json"] {
		log.Printf("[DEBUG] %s: found package.json only -> API_NODE", containerID[:12])
		return types.TypeAPINode
	}

	return types.TypeDocker
}

// checkFilesInContainer 단일 명령으로 여러 파일 존재 여부 확인 (여러 경로 체크)
func (c *Checker) checkFilesInContainer(ctx context.Context, containerID string) map[string]bool {
	if c.client == nil {
		return nil
	}

	// 여러 경로에서 체크 (/app, /opt, /src, /home/*, WORKDIR 등)
	script := `
# 웹서버 설정
echo -n "nginx:" && test -f /etc/nginx/nginx.conf && echo "1" || echo "0"
echo -n "apache:" && (test -f /etc/apache2/apache2.conf || test -f /etc/httpd/conf/httpd.conf) && echo "1" || echo "0"

# Next.js (여러 경로 체크)
echo -n "nextjs:" && (test -f /app/next.config.js || test -f /app/next.config.mjs || test -d /app/.next || \
  test -f /opt/next.config.js || test -d /opt/.next || \
  test -f /src/next.config.js || test -d /src/.next) && echo "1" || echo "0"

# Vite (여러 경로)
echo -n "vite:" && (test -f /app/vite.config.ts || test -f /app/vite.config.js || \
  test -f /opt/vite.config.ts || test -f /opt/vite.config.js || \
  test -f /src/vite.config.ts || test -f /src/vite.config.js) && echo "1" || echo "0"

# React build output
echo -n "react_build:" && (test -f /app/build/index.html || test -f /app/dist/index.html || \
  test -f /usr/share/nginx/html/index.html || test -f /var/www/html/index.html) && echo "1" || echo "0"

# React source
echo -n "react_src:" && (test -f /app/src/main.tsx || test -f /app/src/App.tsx || test -f /app/src/index.tsx || \
  test -f /opt/src/main.tsx || test -f /opt/src/App.tsx) && echo "1" || echo "0"

# Java/Spring (jar, pom.xml, BOOT-INF 등)
echo -n "java:" && (test -f /app/pom.xml || test -f /app/build.gradle || test -d /app/BOOT-INF || \
  test -d /BOOT-INF || ls /*.jar 2>/dev/null | head -1 | grep -q . || ls /app/*.jar 2>/dev/null | head -1 | grep -q . || \
  test -f /opt/pom.xml || test -d /opt/BOOT-INF) && echo "1" || echo "0"

# Go
echo -n "golang:" && (test -f /app/go.mod || test -f /opt/go.mod || test -f /src/go.mod) && echo "1" || echo "0"

# Python (requirements.txt 또는 pyproject.toml)
echo -n "python:" && (test -f /app/requirements.txt || test -f /app/pyproject.toml || \
  test -f /opt/requirements.txt || test -f /opt/pyproject.toml || \
  test -f /requirements.txt || test -f /pyproject.toml || \
  find /home -name "requirements.txt" -maxdepth 3 2>/dev/null | head -1 | grep -q .) && echo "1" || echo "0"

# Python API (main.py, app.py에서 FastAPI/Flask/Django import 확인)
echo -n "python_api:" && ( \
  (test -f /app/main.py && grep -qiE "fastapi|flask|django|uvicorn" /app/main.py 2>/dev/null) || \
  (test -f /app/app.py && grep -qiE "fastapi|flask|django|uvicorn" /app/app.py 2>/dev/null) || \
  (test -f /opt/main.py && grep -qiE "fastapi|flask|django|uvicorn" /opt/main.py 2>/dev/null) || \
  (test -f /main.py && grep -qiE "fastapi|flask|django|uvicorn" /main.py 2>/dev/null) || \
  test -f /app/manage.py) && echo "1" || echo "0"

# Python Module (API가 아닌 Python 스크립트)
echo -n "python_module:" && ( \
  (test -f /app/main.py && ! grep -qiE "fastapi|flask|django|uvicorn" /app/main.py 2>/dev/null) || \
  (test -f /opt/main.py && ! grep -qiE "fastapi|flask|django|uvicorn" /opt/main.py 2>/dev/null)) && echo "1" || echo "0"

# package.json
echo -n "package_json:" && (test -f /app/package.json || test -f /opt/package.json || test -f /src/package.json) && echo "1" || echo "0"

# OCR/AI 관련 (tesseract, opencv, torch 등)
echo -n "ocr_ai:" && (which tesseract >/dev/null 2>&1 || python -c "import cv2" 2>/dev/null || \
  grep -qiE "tesseract|opencv|paddleocr|easyocr|torch|tensorflow" /app/requirements.txt 2>/dev/null || \
  grep -qiE "tesseract|opencv|paddleocr|easyocr|torch|tensorflow" /requirements.txt 2>/dev/null) && echo "1" || echo "0"
`

	execConfig := dockertypes.ExecConfig{
		Cmd:          []string{"sh", "-c", script},
		AttachStdout: true,
		AttachStderr: false,
	}

	execResp, err := c.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return nil
	}

	resp, err := c.client.ContainerExecAttach(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return nil
	}
	defer resp.Close()

	buf := make([]byte, 4096)
	n, _ := resp.Reader.Read(buf)
	output := string(buf[:n])

	// 결과 파싱
	result := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, ":"); idx > 0 {
			key := line[:idx]
			val := strings.TrimSpace(line[idx+1:])
			result[key] = val == "1"
		}
	}

	return result
}

// dirExistsInContainer 컨테이너 내부에 디렉토리가 존재하는지 확인
func (c *Checker) dirExistsInContainer(ctx context.Context, containerID, path string) bool {
	if c.client == nil {
		return false
	}

	execConfig := dockertypes.ExecConfig{
		Cmd:          []string{"test", "-d", path},
		AttachStdout: false,
		AttachStderr: false,
	}

	execResp, err := c.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return false
	}

	err = c.client.ContainerExecStart(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return false
	}

	for i := 0; i < 10; i++ {
		inspect, err := c.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return false
		}
		if !inspect.Running {
			return inspect.ExitCode == 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// fileContains 컨테이너 내부 파일에 특정 문자열이 포함되어 있는지 확인
func (c *Checker) fileContains(ctx context.Context, containerID, path, search string) bool {
	if c.client == nil {
		return false
	}

	execConfig := dockertypes.ExecConfig{
		Cmd:          []string{"grep", "-qi", search, path},
		AttachStdout: false,
		AttachStderr: false,
	}

	execResp, err := c.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return false
	}

	err = c.client.ContainerExecStart(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return false
	}

	for i := 0; i < 10; i++ {
		inspect, err := c.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return false
		}
		if !inspect.Running {
			return inspect.ExitCode == 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// hasJarFiles 컨테이너 내부에 .jar 파일이 있는지 확인
func (c *Checker) hasJarFiles(ctx context.Context, containerID, path string) bool {
	if c.client == nil {
		return false
	}

	execConfig := dockertypes.ExecConfig{
		Cmd:          []string{"sh", "-c", "ls " + path + "/*.jar 2>/dev/null | head -1"},
		AttachStdout: true,
		AttachStderr: false,
	}

	execResp, err := c.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return false
	}

	resp, err := c.client.ContainerExecAttach(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return false
	}
	defer resp.Close()

	// 출력이 있으면 jar 파일이 있는 것
	buf := make([]byte, 256)
	n, _ := resp.Reader.Read(buf)
	return n > 0 && strings.Contains(string(buf[:n]), ".jar")
}

// fileExistsInContainer 컨테이너 내부에 파일이 존재하는지 확인
func (c *Checker) fileExistsInContainer(ctx context.Context, containerID, path string) bool {
	if c.client == nil {
		log.Printf("[DEBUG] fileExistsInContainer: client is nil")
		return false
	}

	execConfig := dockertypes.ExecConfig{
		Cmd:          []string{"test", "-f", path},
		AttachStdout: false,
		AttachStderr: false,
	}

	execResp, err := c.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		log.Printf("[DEBUG] fileExistsInContainer %s %s: exec create failed: %v", containerID[:12], path, err)
		return false
	}

	err = c.client.ContainerExecStart(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		log.Printf("[DEBUG] fileExistsInContainer %s %s: exec start failed: %v", containerID[:12], path, err)
		return false
	}

	// exec 결과 확인 (exit code 0 = 파일 존재)
	for i := 0; i < 10; i++ {
		inspect, err := c.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			log.Printf("[DEBUG] fileExistsInContainer %s %s: exec inspect failed: %v", containerID[:12], path, err)
			return false
		}
		if !inspect.Running {
			return inspect.ExitCode == 0
		}
		time.Sleep(50 * time.Millisecond)
	}

	log.Printf("[DEBUG] fileExistsInContainer %s %s: timeout", containerID[:12], path)
	return false
}

func (c *Checker) checkSpringApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	endpoints := []string{"/actuator/health", "/health", "/"}
	var lastElapsed int
	var lastMsg string
	var lastEndpoint string

	for _, ep := range endpoints {
		url := fmt.Sprintf("http://%s:%d%s", ip, port, ep)
		status, msg, elapsed := c.httpCheck(url)
		lastElapsed = elapsed
		lastMsg = msg
		lastEndpoint = ep

		// UP 또는 WARN(느림) = 응답 받음 → 성공
		if status != types.StatusDown {
			state.Status = status
			state.Message = msg
			state.Endpoint = ep
			state.ResponseTime = elapsed
			return state
		}
	}
	// 모든 endpoint 연결 실패
	state.Status = types.StatusDown
	state.Message = lastMsg
	state.Endpoint = lastEndpoint
	state.ResponseTime = lastElapsed
	return state
}

func (c *Checker) checkWebApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	// HTTPS 포트인 경우 HTTPS로 체크
	protocol := "http"
	if port == 443 {
		protocol = "https"
	}

	pageURL := fmt.Sprintf("%s://%s:%d/", protocol, ip, port)
	status, msg, elapsed, sslError, sslMessage := c.httpCheckWithSSL(pageURL)

	if status != types.StatusDown {
		state.Status = status
		state.Message = msg
		state.Endpoint = "/"
		state.ResponseTime = elapsed
		state.SSLError = sslError
		state.SSLMessage = sslMessage

		// 페이지가 정상이면 리소스 체크 (JS, CSS, 이미지 등)
		if status == types.StatusUp {
			resourceErrors := c.checkWebResourcesStable(state.Name, pageURL)
			if len(resourceErrors) > 0 {
				state.Status = types.StatusWarn
				state.ResourceErrors = resourceErrors
				state.Message = fmt.Sprintf("리소스 에러 %d개 발견", len(resourceErrors))
				log.Printf("[WARN] %s: %d resource errors found", state.Name, len(resourceErrors))
			}
		}
		return state
	}

	// 연결 실패 시 컨테이너의 다른 노출 포트 시도
	for _, p := range cont.Ports {
		tryPort := int(p.PrivatePort)
		if tryPort == port {
			continue
		}
		tryProtocol := "http"
		if tryPort == 443 {
			tryProtocol = "https"
		}
		fallbackURL := fmt.Sprintf("%s://%s:%d/", tryProtocol, ip, tryPort)
		fbStatus, fbMsg, fbElapsed, fbSSLError, fbSSLMessage := c.httpCheckWithSSL(fallbackURL)
		if fbStatus != types.StatusDown {
			state.Status = fbStatus
			state.Message = fbMsg
			state.Port = tryPort
			state.Endpoint = "/"
			state.ResponseTime = fbElapsed
			state.SSLError = fbSSLError
			state.SSLMessage = fbSSLMessage
			return state
		}
	}

	// 일반적인 웹 포트 시도 (Next.js, React 등 포함)
	fallbackPorts := []int{3000, 8080, 80, 443, 8000, 5000, 11242, 11240, 11241, 11243, 11244, 11245}
	for _, fp := range fallbackPorts {
		if fp == port {
			continue
		}
		tryProtocol := "http"
		if fp == 443 {
			tryProtocol = "https"
		}
		fallbackURL := fmt.Sprintf("%s://%s:%d/", tryProtocol, ip, fp)
		fbStatus, fbMsg, fbElapsed, fbSSLError, fbSSLMessage := c.httpCheckWithSSL(fallbackURL)
		if fbStatus != types.StatusDown {
			state.Status = fbStatus
			state.Message = fbMsg
			state.Port = fp
			state.Endpoint = "/"
			state.ResponseTime = fbElapsed
			state.SSLError = fbSSLError
			state.SSLMessage = fbSSLMessage
			return state
		}
	}

	state.Status = status
	state.Message = msg
	state.Endpoint = "/"
	state.ResponseTime = elapsed
	state.SSLError = sslError
	state.SSLMessage = sslMessage
	return state
}

func (c *Checker) checkAPIApp(ctx context.Context, cont dockertypes.Container, state types.ServiceState) types.ServiceState {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	endpoints := []string{"/health", "/api/health", "/"}
	var lastElapsed int
	var lastMsg string
	var lastEndpoint string

	// 1. 기본 포트로 시도
	for _, ep := range endpoints {
		url := fmt.Sprintf("http://%s:%d%s", ip, port, ep)
		status, msg, elapsed := c.httpCheck(url)
		lastElapsed = elapsed
		lastMsg = msg
		lastEndpoint = ep

		if status != types.StatusDown {
			state.Status = status
			state.Message = msg
			state.Endpoint = ep
			state.ResponseTime = elapsed
			return state
		}
	}

	// 2. 기본 포트 실패 시 컨테이너의 다른 노출 포트 시도
	for _, p := range cont.Ports {
		tryPort := int(p.PrivatePort)
		if tryPort == port {
			continue
		}
		url := fmt.Sprintf("http://%s:%d/", ip, tryPort)
		status, msg, elapsed := c.httpCheck(url)
		if status != types.StatusDown {
			state.Status = status
			state.Message = msg
			state.Endpoint = "/"
			state.Port = tryPort
			state.ResponseTime = elapsed
			return state
		}
	}

	// 3. 여전히 실패 시 일반적인 포트 시도 (최대 5개)
	fallbackPorts := []int{8080, 3000, 8000, 5000, 80}
	for _, fp := range fallbackPorts {
		if fp == port {
			continue
		}
		url := fmt.Sprintf("http://%s:%d/", ip, fp)
		status, msg, elapsed := c.httpCheck(url)
		if status != types.StatusDown {
			state.Status = status
			state.Message = msg
			state.Endpoint = "/"
			state.Port = fp
			state.ResponseTime = elapsed
			return state
		}
	}

	state.Status = types.StatusDown
	state.Message = lastMsg
	state.Endpoint = lastEndpoint
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

	state.Port = port
	state.Host = ip

	// 타입별 실제 체크
	switch state.Type {
	case types.TypeRedis:
		return c.checkRedis(ip, port, state)
	case types.TypeMySQL:
		return c.checkMySQL(ip, port, state)
	case types.TypePostgreSQL:
		return c.checkPostgreSQL(ip, port, state)
	case types.TypeMongoDB:
		return c.checkMongoDB(ip, port, state)
	default:
		return c.checkTCPPort(ip, port, state)
	}
}

// checkRedis Redis PING 명령으로 실제 동작 확인
func (c *Checker) checkRedis(ip string, port int, state types.ServiceState) types.ServiceState {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("Redis 연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	defer conn.Close()

	// Redis PING 명령 전송 (RESP 프로토콜)
	conn.SetDeadline(time.Now().Add(c.timeout))
	_, err = conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("Redis PING 전송 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}

	// 응답 읽기
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("Redis 응답 실패: %v", err)
		state.ResponseTime = elapsed
		return state
	}

	response := string(buf[:n])
	if strings.Contains(response, "PONG") || strings.Contains(response, "+PONG") {
		state.Status = types.StatusUp
		state.Message = "Redis PONG 응답 정상"
		state.ResponseTime = elapsed
		return state
	}

	// 인증 필요한 경우
	if strings.Contains(response, "NOAUTH") || strings.Contains(response, "AUTH") {
		state.Status = types.StatusWarn
		state.Message = "Redis 인증 필요 (서버 응답 중)"
		state.ResponseTime = elapsed
		return state
	}

	state.Status = types.StatusWarn
	state.Message = fmt.Sprintf("Redis 응답: %s", strings.TrimSpace(response))
	state.ResponseTime = elapsed
	return state
}

// checkMySQL MySQL 프로토콜 핸드셰이크 확인
func (c *Checker) checkMySQL(ip string, port int, state types.ServiceState) types.ServiceState {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("MySQL 연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	defer conn.Close()

	// MySQL 서버는 연결 시 핸드셰이크 패킷을 보냄
	conn.SetDeadline(time.Now().Add(c.timeout))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("MySQL 핸드셰이크 실패: %v", err)
		state.ResponseTime = elapsed
		return state
	}

	// MySQL 핸드셰이크 패킷 확인 (최소 4바이트, 프로토콜 버전 포함)
	if n >= 5 && buf[4] == 10 { // Protocol version 10
		// 서버 버전 문자열 추출 (null-terminated)
		version := ""
		for i := 5; i < n && buf[i] != 0; i++ {
			version += string(buf[i])
		}
		state.Status = types.StatusUp
		state.Message = fmt.Sprintf("MySQL %s 응답 정상", version)
		state.ResponseTime = elapsed
		return state
	}

	state.Status = types.StatusUp
	state.Message = "MySQL 연결 정상"
	state.ResponseTime = elapsed
	return state
}

// checkPostgreSQL PostgreSQL 연결 확인
func (c *Checker) checkPostgreSQL(ip string, port int, state types.ServiceState) types.ServiceState {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("PostgreSQL 연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	defer conn.Close()

	// PostgreSQL 시작 메시지 전송 (SSLRequest)
	conn.SetDeadline(time.Now().Add(c.timeout))
	// SSLRequest: length(8) + SSL code(80877103)
	sslRequest := []byte{0, 0, 0, 8, 4, 210, 22, 47}
	_, err = conn.Write(sslRequest)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("PostgreSQL 요청 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}

	// 응답 읽기 (S=SSL지원, N=SSL미지원, E=에러)
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("PostgreSQL 응답 실패: %v", err)
		state.ResponseTime = elapsed
		return state
	}

	if buf[0] == 'S' || buf[0] == 'N' {
		state.Status = types.StatusUp
		state.Message = "PostgreSQL 응답 정상"
		state.ResponseTime = elapsed
		return state
	}

	state.Status = types.StatusUp
	state.Message = "PostgreSQL 연결 정상"
	state.ResponseTime = elapsed
	return state
}

// checkMongoDB MongoDB 연결 확인
func (c *Checker) checkMongoDB(ip string, port int, state types.ServiceState) types.ServiceState {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("MongoDB 연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	defer conn.Close()
	elapsed := int(time.Since(start).Milliseconds())

	// MongoDB는 복잡한 프로토콜이므로 TCP 연결 성공만 확인
	state.Status = types.StatusUp
	state.Message = "MongoDB 연결 정상"
	state.ResponseTime = elapsed
	return state
}

// checkTCPPort 단순 TCP 포트 연결 확인
func (c *Checker) checkTCPPort(ip string, port int, state types.ServiceState) types.ServiceState {
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
	state.ResponseTime = elapsed
	return state
}

// httpCheck HTTP 요청을 통해 상태를 확인하고 응답 시간을 반환
// DOWN = 연결 실패 (timeout, connection refused)
// UP = 2xx 응답
// WARN = 4xx/5xx 응답 (서버는 응답함, 확인 필요)
func (c *Checker) httpCheck(url string) (types.Status, string, int) {
	status, msg, elapsed, _, _ := c.httpCheckWithSSL(url)
	return status, msg, elapsed
}

// httpCheckWithSSL HTTP 요청을 통해 상태를 확인하고 SSL 오류 정보도 반환
func (c *Checker) httpCheckWithSSL(url string) (types.Status, string, int, bool, string) {
	log.Printf("[DEBUG] HTTP check: %s", url)
	var sslError bool
	var sslMessage string

	// HTTPS URL인 경우 SSL 인증서 검증
	if strings.HasPrefix(url, "https://") {
		sslError, sslMessage = c.checkSSL(url)
	}

	// InsecureSkipVerify로 실제 서비스 상태 확인
	client := &http.Client{
		Timeout: c.timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	start := time.Now()
	resp, err := client.Get(url)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[DEBUG] HTTP failed: %s (%dms) - %v", url, elapsed, err)
		return types.StatusDown, fmt.Sprintf("연결 실패: %v", err), elapsed, sslError, sslMessage
	}
	defer resp.Body.Close()

	statusCode := resp.StatusCode
	log.Printf("[DEBUG] HTTP response: %s (%dms) - status %d", url, elapsed, statusCode)

	// 2xx = 정상 (SSL 오류가 있으면 WARN)
	if statusCode >= 200 && statusCode < 300 {
		if sslError {
			return types.StatusWarn, fmt.Sprintf("%d OK (SSL 오류)", statusCode), elapsed, sslError, sslMessage
		}
		return types.StatusUp, fmt.Sprintf("%d OK", statusCode), elapsed, sslError, sslMessage
	}

	// 401/403 = 인증 필요 (서버는 살아있음)
	if statusCode == 401 || statusCode == 403 {
		return types.StatusWarn, fmt.Sprintf("%d 인증필요", statusCode), elapsed, sslError, sslMessage
	}

	// 4xx/5xx = 서버 응답함, 확인 필요
	return types.StatusWarn, fmt.Sprintf("%d %s", statusCode, resp.Status), elapsed, sslError, sslMessage
}

// checkSSL SSL 인증서 검증
func (c *Checker) checkSSL(url string) (bool, string) {
	client := &http.Client{
		Timeout: c.timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		},
	}

	resp, err := client.Head(url)
	if err != nil {
		errStr := err.Error()
		// SSL 인증서 관련 오류 감지
		if strings.Contains(errStr, "certificate") ||
			strings.Contains(errStr, "x509") ||
			strings.Contains(errStr, "tls") ||
			strings.Contains(errStr, "SSL") {

			if strings.Contains(errStr, "expired") {
				return true, "SSL 인증서 만료"
			} else if strings.Contains(errStr, "self signed") || strings.Contains(errStr, "self-signed") {
				return true, "자체 서명 인증서"
			} else if strings.Contains(errStr, "unknown authority") {
				return true, "신뢰할 수 없는 인증 기관"
			} else if strings.Contains(errStr, "hostname") || strings.Contains(errStr, "doesn't match") {
				return true, "SSL 인증서 도메인 불일치"
			} else {
				return true, "SSL 인증서 오류"
			}
		}
		return false, ""
	}
	defer resp.Body.Close()
	return false, ""
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

// checkWebResourcesStable 리소스 에러 상태를 안정화하여 반환
// 에러가 발생하면 즉시 반영하지만, 복구는 3회 연속 정상이어야 반영
func (c *Checker) checkWebResourcesStable(serviceID, pageURL string) []types.ResourceError {
	currentErrors := c.checkWebResources(pageURL)

	// 캐시 확인
	cached, exists := c.resourceErrorCache[serviceID]

	if len(currentErrors) > 0 {
		// 에러 발견 → 즉시 캐시 업데이트하고 반환
		c.resourceErrorCache[serviceID] = &resourceErrorState{
			errors:        currentErrors,
			consecutiveOK: 0,
			lastCheckedAt: time.Now(),
		}
		return currentErrors
	}

	// 현재는 정상
	if !exists || len(cached.errors) == 0 {
		// 이전에도 정상이었으면 그냥 빈 배열 반환
		return nil
	}

	// 이전에 에러가 있었고 현재는 정상
	cached.consecutiveOK++
	cached.lastCheckedAt = time.Now()

	// 3회 연속 정상이면 복구로 처리
	if cached.consecutiveOK >= 3 {
		log.Printf("[INFO] %s: resource errors cleared after 3 consecutive OK checks", serviceID)
		delete(c.resourceErrorCache, serviceID)
		return nil
	}

	// 아직 안정화 안됨 - 이전 에러 유지
	log.Printf("[DEBUG] %s: waiting for stable recovery (%d/3 OK)", serviceID, cached.consecutiveOK)
	return cached.errors
}

// checkWebResources 웹 페이지 진입 시 모든 네트워크 리소스 상태 체크
// Chrome이 있으면 실제 브라우저로 모든 네트워크 요청 캡처
// Chrome이 없으면 HTML 파싱으로 fallback
func (c *Checker) checkWebResources(pageURL string) []types.ResourceError {
	// Chrome이 설치되어 있으면 브라우저 기반 체크
	if c.browserChecker != nil && c.browserChecker.IsAvailable() {
		errors, err := c.browserChecker.CheckPageResources(pageURL)
		if err != nil {
			log.Printf("[WARN] Browser check failed, falling back to HTML parsing: %v", err)
			return c.checkWebResourcesFallback(pageURL)
		}
		return errors
	}

	// Chrome이 없으면 HTML 파싱 fallback
	return c.checkWebResourcesFallback(pageURL)
}

// checkWebResourcesFallback HTML 파싱 기반 리소스 체크 (Chrome 없을 때 사용)
func (c *Checker) checkWebResourcesFallback(pageURL string) []types.ResourceError {
	var errors []types.ResourceError

	client := &http.Client{
		Timeout: c.timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// 페이지 HTML 가져오기
	resp, err := client.Get(pageURL)
	if err != nil {
		return errors
	}
	defer resp.Body.Close()

	// HTML 읽기 (최대 2MB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return errors
	}
	html := string(body)

	// 모든 네트워크 리소스 URL 추출 패턴
	patterns := map[string]*regexp.Regexp{
		// JavaScript
		"js": regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`),
		// CSS (stylesheet)
		"css": regexp.MustCompile(`<link[^>]+href=["']([^"']+)["'][^>]*rel=["']stylesheet["']|<link[^>]+rel=["']stylesheet["'][^>]*href=["']([^"']+)["']`),
		// 이미지
		"img": regexp.MustCompile(`<img[^>]+src=["']([^"']+)["']`),
		// srcset 이미지
		"img-srcset": regexp.MustCompile(`srcset=["']([^"']+)["']`),
		// 폰트/아이콘 (preload, prefetch)
		"font": regexp.MustCompile(`<link[^>]+href=["']([^"']+\.(woff2?|ttf|eot|otf)[^"']*)["']`),
		// Favicon, 아이콘
		"icon": regexp.MustCompile(`<link[^>]+href=["']([^"']+)["'][^>]*rel=["'](icon|shortcut icon|apple-touch-icon)["']|<link[^>]+rel=["'](icon|shortcut icon|apple-touch-icon)["'][^>]*href=["']([^"']+)["']`),
		// 비디오
		"video": regexp.MustCompile(`<video[^>]+src=["']([^"']+)["']|<source[^>]+src=["']([^"']+)["']`),
		// 오디오
		"audio": regexp.MustCompile(`<audio[^>]+src=["']([^"']+)["']`),
		// iframe
		"iframe": regexp.MustCompile(`<iframe[^>]+src=["']([^"']+)["']`),
		// object/embed
		"embed": regexp.MustCompile(`<(?:object|embed)[^>]+(?:src|data)=["']([^"']+)["']`),
		// background-image in style
		"bg-img": regexp.MustCompile(`url\(["']?([^"')]+)["']?\)`),
		// preload/prefetch resources
		"preload": regexp.MustCompile(`<link[^>]+href=["']([^"']+)["'][^>]*rel=["'](?:preload|prefetch)["']|<link[^>]+rel=["'](?:preload|prefetch)["'][^>]*href=["']([^"']+)["']`),
		// manifest
		"manifest": regexp.MustCompile(`<link[^>]+href=["']([^"']+)["'][^>]*rel=["']manifest["']`),
	}

	baseURL, _ := url.Parse(pageURL)
	checked := make(map[string]bool) // 중복 체크 방지

	for resType, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(html, -1)
		for _, match := range matches {
			// 매칭된 그룹들 중 비어있지 않은 URL 찾기
			for i := 1; i < len(match); i++ {
				resourceURL := strings.TrimSpace(match[i])
				if resourceURL == "" {
					continue
				}

				// srcset인 경우 여러 URL 파싱
				if resType == "img-srcset" {
					srcsetURLs := c.parseSrcset(resourceURL)
					for _, srcURL := range srcsetURLs {
						c.checkAndAddError(srcURL, "img", baseURL, checked, &errors)
					}
					continue
				}

				c.checkAndAddError(resourceURL, resType, baseURL, checked, &errors)
			}
		}
	}

	return errors
}

// parseSrcset srcset 속성에서 URL들을 추출
func (c *Checker) parseSrcset(srcset string) []string {
	var urls []string
	// srcset 형식: "url1 1x, url2 2x" 또는 "url1 100w, url2 200w"
	parts := strings.Split(srcset, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// 공백으로 분리하여 첫 번째가 URL
		fields := strings.Fields(part)
		if len(fields) > 0 {
			urls = append(urls, fields[0])
		}
	}
	return urls
}

// checkAndAddError 리소스 URL 체크 후 에러 추가
func (c *Checker) checkAndAddError(resourceURL, resType string, baseURL *url.URL, checked map[string]bool, errors *[]types.ResourceError) {
	// HTML 엔티티 디코딩 (&amp; -> &, &lt; -> < 등)
	resourceURL = html.UnescapeString(resourceURL)

	// 스킵할 URL 패턴
	if strings.HasPrefix(resourceURL, "data:") ||
		strings.HasPrefix(resourceURL, "blob:") ||
		strings.HasPrefix(resourceURL, "javascript:") ||
		strings.HasPrefix(resourceURL, "mailto:") ||
		strings.HasPrefix(resourceURL, "#") {
		return
	}

	// 상대 경로를 절대 경로로 변환
	resourceURL = c.resolveURL(baseURL, resourceURL)

	// 중복 체크
	if checked[resourceURL] {
		return
	}
	checked[resourceURL] = true

	// 리소스 상태 체크 (HEAD 요청)
	statusCode := c.checkResourceStatus(resourceURL)
	if statusCode >= 400 {
		*errors = append(*errors, types.ResourceError{
			URL:        resourceURL,
			StatusCode: statusCode,
			Type:       resType,
		})
		log.Printf("[WARN] Resource error: %s %d (%s)", resType, statusCode, resourceURL)
	}
}

// resolveURL 상대 경로를 절대 경로로 변환
func (c *Checker) resolveURL(base *url.URL, ref string) string {
	// 이미 절대 URL인 경우
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	// //로 시작하는 경우 (프로토콜 상대 URL)
	if strings.HasPrefix(ref, "//") {
		return base.Scheme + ":" + ref
	}
	// 상대 경로
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(refURL).String()
}

// checkResourceStatus 리소스 URL의 HTTP 상태 코드 확인
func (c *Checker) checkResourceStatus(resourceURL string) int {
	client := &http.Client{
		Timeout: 3 * time.Second, // 리소스는 빠르게 체크
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequest("HEAD", resourceURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "HealthAgent/1.0")

	resp, err := client.Do(req)
	if err != nil {
		// 연결 실패는 500으로 처리
		return 500
	}
	defer resp.Body.Close()

	return resp.StatusCode
}
