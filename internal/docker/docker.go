package docker

import (
	"context"
	"fmt"
	"log"
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

	url := fmt.Sprintf("http://%s:%d/", ip, port)
	status, msg, elapsed := c.httpCheck(url)

	if status != types.StatusDown {
		state.Status = status
		state.Message = msg
		state.Endpoint = "/"
		state.ResponseTime = elapsed
		return state
	}

	// 연결 실패 시 컨테이너의 다른 노출 포트 시도
	for _, p := range cont.Ports {
		tryPort := int(p.PrivatePort)
		if tryPort == port {
			continue
		}
		fallbackURL := fmt.Sprintf("http://%s:%d/", ip, tryPort)
		fbStatus, fbMsg, fbElapsed := c.httpCheck(fallbackURL)
		if fbStatus != types.StatusDown {
			state.Status = fbStatus
			state.Message = fbMsg
			state.Port = tryPort
			state.Endpoint = "/"
			state.ResponseTime = fbElapsed
			return state
		}
	}

	// 일반적인 웹 포트 시도 (Next.js, React 등 포함)
	fallbackPorts := []int{3000, 8080, 80, 8000, 5000, 11242, 11240, 11241, 11243, 11244, 11245}
	for _, fp := range fallbackPorts {
		if fp == port {
			continue
		}
		fallbackURL := fmt.Sprintf("http://%s:%d/", ip, fp)
		fbStatus, fbMsg, fbElapsed := c.httpCheck(fallbackURL)
		if fbStatus != types.StatusDown {
			state.Status = fbStatus
			state.Message = fbMsg
			state.Port = fp
			state.Endpoint = "/"
			state.ResponseTime = fbElapsed
			return state
		}
	}

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
// DOWN = 연결 실패 (timeout, connection refused)
// UP = 2xx 응답
// WARN = 4xx/5xx 응답 (서버는 응답함, 확인 필요)
func (c *Checker) httpCheck(url string) (types.Status, string, int) {
	log.Printf("[DEBUG] HTTP check: %s", url)
	client := &http.Client{Timeout: c.timeout}
	start := time.Now()
	resp, err := client.Get(url)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[DEBUG] HTTP failed: %s (%dms) - %v", url, elapsed, err)
		return types.StatusDown, fmt.Sprintf("연결 실패: %v", err), elapsed
	}
	defer resp.Body.Close()

	statusCode := resp.StatusCode
	log.Printf("[DEBUG] HTTP response: %s (%dms) - status %d", url, elapsed, statusCode)

	// 2xx = 정상
	if statusCode >= 200 && statusCode < 300 {
		return types.StatusUp, fmt.Sprintf("%d OK", statusCode), elapsed
	}

	// 401/403 = 인증 필요 (서버는 살아있음)
	if statusCode == 401 || statusCode == 403 {
		return types.StatusWarn, fmt.Sprintf("%d 인증필요", statusCode), elapsed
	}

	// 4xx/5xx = 서버 응답함, 확인 필요
	return types.StatusWarn, fmt.Sprintf("%d %s", statusCode, resp.Status), elapsed
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
