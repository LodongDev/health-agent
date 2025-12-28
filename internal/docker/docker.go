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
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
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
	httpClient       *http.Client         // 공유 HTTP 클라이언트 (연결 재사용)
	timeout          time.Duration
	lastResults      []types.ServiceState // 마지막 성공 결과 캐시
	lastRunningNames map[string]bool      // 이전에 실행 중이었던 컨테이너 이름
	browserChecker   *browser.Checker     // 브라우저 기반 네트워크 체커
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

	// 공유 HTTP 클라이언트 (연결 풀 설정으로 "too many open files" 방지)
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:        100,              // 최대 유휴 연결 수
			MaxIdleConnsPerHost: 10,               // 호스트당 최대 유휴 연결
			MaxConnsPerHost:     20,               // 호스트당 최대 연결 수
			IdleConnTimeout:     30 * time.Second, // 유휴 연결 타임아웃
			DisableKeepAlives:   false,            // Keep-Alive 활성화
		},
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
		return &Checker{timeout: 5 * time.Second, httpClient: httpClient, browserChecker: browserChk}
	}
	return &Checker{client: cli, timeout: 5 * time.Second, httpClient: httpClient, browserChecker: browserChk}
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

// createClosedState 수동 종료된 컨테이너의 상태 생성 (exited 상태로 API에 전달)
func (c *Checker) createClosedState(name string, cont dockertypes.Container) types.ServiceState {
	return types.ServiceState{
		ID:             name,
		Name:           name,
		Type:           types.TypeDocker,
		CheckedAt:      time.Now(),
		ContainerState: cont.State, // "exited"
		Path:           cont.Image,
	}
}

// 기본 무시 패턴 (항상 적용)
var defaultIgnorePatterns = []string{
	"*temp*", // temp 포함 컨테이너 제외
}

// isInIgnoreList 컨테이너 이름이 무시 목록에 있는지 확인
// 패턴 지원:
//   - "nginx-dev"  : 정확히 일치
//   - "dev-*"      : dev-로 시작하는 모든 컨테이너
//   - "*-dev"      : -dev로 끝나는 모든 컨테이너
//   - "*test*"     : test를 포함하는 모든 컨테이너
func isInIgnoreList(name string, ignoreList []string) bool {
	// 기본 무시 패턴 먼저 확인
	for _, pattern := range defaultIgnorePatterns {
		if matchPattern(name, pattern) {
			return true
		}
	}
	// 사용자 설정 무시 목록 확인
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

	// 서비스 ID = 컨테이너 이름 (serverIp + name으로 고유성 보장)
	state := types.ServiceState{
		ID:             name,
		Name:           name,
		Type:           svcType,
		CheckedAt:      time.Now(),
		ContainerState: cont.State, // running, exited, etc.
		Path:           cont.Image,
	}

	// 컨테이너 상세 정보 가져오기
	inspect, err := c.client.ContainerInspect(ctx, cont.ID)
	if err == nil {
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
	}

	// 컨테이너가 running이 아니면 HTTP 체크 안함
	if cont.State != "running" {
		log.Printf("[DEBUG] Container %s: state=%s (not running, skip HTTP check)", name, cont.State)
		return state
	}

	// 서비스 타입별 HTTP 체크 (raw 데이터 수집)
	log.Printf("[DEBUG] Container %s: type=%s, image=%s", name, svcType, cont.Image)
	switch svcType {
	case types.TypeAPIJava:
		state.HttpCheck = c.checkHTTP(ctx, cont, []string{"/actuator/health", "/health", "/"})
	case types.TypeWebNginx, types.TypeWebApache, types.TypeWeb:
		state.HttpCheck = c.checkHTTP(ctx, cont, []string{"/"})
		// 웹 서비스는 리소스 체크도 수행
		if state.HttpCheck != nil && state.HttpCheck.Success {
			state.ResourceChecks = c.checkWebResources(ctx, cont)
		}
	case types.TypeAPI, types.TypeAPIPython, types.TypeAPINode, types.TypeAPIGo:
		state.HttpCheck = c.checkHTTP(ctx, cont, []string{"/health", "/api/health", "/"})
	case types.TypeMySQL, types.TypePostgreSQL, types.TypeRedis, types.TypeMongoDB:
		state.HttpCheck = c.checkDBConnection(ctx, cont, svcType)
	default:
		// 기본: HTTP 체크 안함, 컨테이너 상태만 전송
		log.Printf("[DEBUG] %s -> no HTTP check (type=%s)", name, svcType)
	}

	if state.HttpCheck != nil {
		log.Printf("[DEBUG] %s: httpCheck success=%v, statusCode=%d, responseTime=%dms",
			name, state.HttpCheck.Success, state.HttpCheck.StatusCode, state.HttpCheck.ResponseTime)
	}
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

// checkHTTP HTTP 요청으로 raw 데이터 수집 (상태 판정은 API에서)
func (c *Checker) checkHTTP(ctx context.Context, cont dockertypes.Container, endpoints []string) *types.CheckResult {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)

	// HTTPS 포트인 경우
	protocol := "http"
	if port == 443 {
		protocol = "https"
	}

	for _, ep := range endpoints {
		checkURL := fmt.Sprintf("%s://%s:%d%s", protocol, ip, port, ep)
		result := c.doHTTPCheck(checkURL)

		// 연결 성공하면 반환 (상태 코드와 관계없이)
		if result.Success {
			return result
		}
	}

	// 모든 endpoint 실패 시 마지막 결과 반환
	checkURL := fmt.Sprintf("%s://%s:%d/", protocol, ip, port)
	return c.doHTTPCheck(checkURL)
}

// doHTTPCheck 단일 URL에 대한 HTTP 체크 (raw 데이터)
func (c *Checker) doHTTPCheck(checkURL string) *types.CheckResult {
	start := time.Now()

	resp, err := c.httpClient.Get(checkURL)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		return &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
	}
	// Body를 완전히 읽어서 연결 재사용 가능하게 함
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	return &types.CheckResult{
		Success:      true,
		StatusCode:   resp.StatusCode,
		ResponseTime: elapsed,
	}
}

// checkDBConnection DB 연결 체크 (raw 데이터)
func (c *Checker) checkDBConnection(ctx context.Context, cont dockertypes.Container, svcType types.ServiceType) *types.CheckResult {
	ip := c.getContainerIP(ctx, cont.ID)
	var port int

	switch svcType {
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

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), c.timeout)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		return &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
	}
	conn.Close()

	return &types.CheckResult{
		Success:      true,
		StatusCode:   200, // TCP 연결 성공
		ResponseTime: elapsed,
	}
}

// checkWebResources 웹 리소스 체크 (raw 데이터, 모든 리소스)
func (c *Checker) checkWebResources(ctx context.Context, cont dockertypes.Container) []types.ResourceCheck {
	ip := c.getContainerIP(ctx, cont.ID)
	port := c.getHTTPPort(cont)
	protocol := "http"
	if port == 443 {
		protocol = "https"
	}
	pageURL := fmt.Sprintf("%s://%s:%d/", protocol, ip, port)

	return c.fetchAndCheckResources(pageURL)
}

// fetchAndCheckResources HTML에서 리소스 추출하고 체크
func (c *Checker) fetchAndCheckResources(pageURL string) []types.ResourceCheck {
	var results []types.ResourceCheck

	// 페이지 HTML 가져오기 (공유 HTTP 클라이언트 사용)
	resp, err := c.httpClient.Get(pageURL)
	if err != nil {
		return results
	}
	defer resp.Body.Close()

	// HTML 읽기 (최대 2MB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return results
	}
	htmlContent := string(body)

	// 리소스 URL 추출 패턴
	patterns := map[string]*regexp.Regexp{
		"js":  regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`),
		"css": regexp.MustCompile(`<link[^>]+href=["']([^"']+)["'][^>]*rel=["']stylesheet["']`),
		"img": regexp.MustCompile(`<img[^>]+src=["']([^"']+)["']`),
	}

	baseURL, _ := url.Parse(pageURL)
	checked := make(map[string]bool)

	for resType, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(htmlContent, -1)
		for _, match := range matches {
			if len(match) < 2 || match[1] == "" {
				continue
			}

			resourceURL := html.UnescapeString(strings.TrimSpace(match[1]))

			// 스킵할 URL
			if strings.HasPrefix(resourceURL, "data:") || strings.HasPrefix(resourceURL, "blob:") {
				continue
			}

			// 절대 경로로 변환
			resourceURL = c.resolveURL(baseURL, resourceURL)

			// 중복 체크
			if checked[resourceURL] {
				continue
			}
			checked[resourceURL] = true

			// 리소스 상태 체크
			statusCode := c.getResourceStatus(resourceURL, pageURL)
			results = append(results, types.ResourceCheck{
				URL:        resourceURL,
				StatusCode: statusCode,
				Type:       resType,
			})
		}
	}

	return results
}

// getResourceStatus 리소스 HTTP 상태 코드 확인 (개선된 버전)
func (c *Checker) getResourceStatus(resourceURL, referer string) int {
	req, err := http.NewRequest("GET", resourceURL, nil)
	if err != nil {
		return 0
	}

	// 실제 브라우저처럼 헤더 설정
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0 // 연결 실패
	}
	// Body를 완전히 읽어서 연결 재사용 가능하게 함
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	resp.Body.Close()

	return resp.StatusCode
}

// resolveURL 상대 경로를 절대 경로로 변환
func (c *Checker) resolveURL(base *url.URL, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "//") {
		return base.Scheme + ":" + ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(refURL).String()
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

// ContainerEvent 컨테이너 이벤트 정보
type ContainerEvent struct {
	Name   string    // 컨테이너 이름
	Action string    // stop, die, start 등
	Time   time.Time // 이벤트 발생 시간
}

// StartEventsListener Docker 이벤트 리스너 시작
// 컨테이너 stop/die 이벤트 발생 시 콜백 호출
func (c *Checker) StartEventsListener(ctx context.Context, callback func(ContainerEvent)) error {
	if c.client == nil {
		return fmt.Errorf("Docker 클라이언트 없음")
	}

	// 컨테이너 이벤트만 필터링 (stop, die)
	filterArgs := filters.NewArgs()
	filterArgs.Add("type", "container")
	filterArgs.Add("event", "stop")
	filterArgs.Add("event", "die")

	eventsChan, errChan := c.client.Events(ctx, dockertypes.EventsOptions{
		Filters: filterArgs,
	})

	go func() {
		log.Println("[INFO] Docker events listener started")
		for {
			select {
			case <-ctx.Done():
				log.Println("[INFO] Docker events listener stopped")
				return
			case event := <-eventsChan:
				c.handleDockerEvent(event, callback)
			case err := <-errChan:
				if err != nil && ctx.Err() == nil {
					log.Printf("[WARN] Docker events error: %v", err)
				}
				return
			}
		}
	}()

	return nil
}

// handleDockerEvent Docker 이벤트 처리
func (c *Checker) handleDockerEvent(event events.Message, callback func(ContainerEvent)) {
	name := event.Actor.Attributes["name"]
	if name == "" {
		return
	}

	// 무시 목록 확인
	ignoreList := config.GetIgnoreList()
	if isInIgnoreList(name, ignoreList) {
		log.Printf("[DEBUG] Ignoring event for: %s", name)
		return
	}

	log.Printf("[INFO] Docker event: %s %s", event.Action, name)

	callback(ContainerEvent{
		Name:   name,
		Action: event.Action,
		Time:   time.Unix(event.Time, 0),
	})
}

// GetContainerState 특정 컨테이너의 현재 상태 조회
func (c *Checker) GetContainerState(ctx context.Context, name string) *types.ServiceState {
	if c.client == nil {
		return nil
	}

	containers, err := c.client.ContainerList(ctx, dockertypes.ContainerListOptions{All: true})
	if err != nil {
		return nil
	}

	for _, cont := range containers {
		contName := strings.TrimPrefix(cont.Names[0], "/")
		if contName == name {
			return &types.ServiceState{
				ID:             fmt.Sprintf("%s_%s", getMachineID(), contName),
				Name:           contName,
				Type:           c.detectServiceType(cont),
				CheckedAt:      time.Now(),
				ContainerState: cont.State, // running, exited 등
				Path:           cont.Image,
			}
		}
	}

	return nil
}
