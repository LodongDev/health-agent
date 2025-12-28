package oscheck

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"health-agent/internal/types"
)

type Checker struct {
	timeout time.Duration
}

func New() *Checker {
	return &Checker{timeout: 5 * time.Second}
}

func (c *Checker) CheckAll() []types.ServiceState {
	var results []types.ServiceState
	// Database
	if r := c.CheckMySQL(); r != nil {
		results = append(results, *r)
	}
	if r := c.CheckPostgreSQL(); r != nil {
		results = append(results, *r)
	}
	if r := c.CheckRedis(); r != nil {
		results = append(results, *r)
	}
	if r := c.CheckMongoDB(); r != nil {
		results = append(results, *r)
	}
	// Web Server
	if r := c.CheckNginx(); r != nil {
		results = append(results, *r)
	}
	if r := c.CheckHTTPD(); r != nil {
		results = append(results, *r)
	}
	return results
}

func (c *Checker) CheckMySQL() *types.ServiceState {
	port, configPath := c.getMySQLPortAndPath()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-mysql", Name: "MySQL (OS)", Type: types.TypeMySQL,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("mysqld", "mysql"),
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	conn.Close()
	if c.commandExists("mysqladmin") {
		cmd := exec.Command("mysqladmin", "ping", "-h", "localhost", fmt.Sprintf("-P%d", port))
		if err := cmd.Run(); err != nil {
			state.Status = types.StatusWarn
			state.Message = fmt.Sprintf("포트 %d 연결됨, ping 실패", port)
			state.ResponseTime = int(time.Since(start).Milliseconds())
			return state
		}
	}
	state.Status = types.StatusUp
	state.Message = fmt.Sprintf("포트 %d 정상", port)
	state.ResponseTime = int(time.Since(start).Milliseconds())
	return state
}

func (c *Checker) getMySQLPortAndPath() (int, string) {
	paths := []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "/etc/mysql/mysql.conf.d/mysqld.cnf"}
	for _, p := range paths {
		if port := c.parseConfigPort(p, "port"); port > 0 {
			return port, p
		}
		// 설정 파일은 존재하지만 포트 설정이 없는 경우
		if _, err := os.Stat(p); err == nil {
			if c.isPortListening(3306) {
				return 3306, p
			}
		}
	}
	if c.isPortListening(3306) {
		return 3306, ""
	}
	return 0, ""
}

func (c *Checker) CheckPostgreSQL() *types.ServiceState {
	port, configPath := c.getPostgreSQLPortAndPath()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-postgresql", Name: "PostgreSQL (OS)", Type: types.TypePostgreSQL,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("postgres", "postgresql"),
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	conn.Close()
	if c.commandExists("pg_isready") {
		cmd := exec.Command("pg_isready", "-h", "localhost", "-p", strconv.Itoa(port))
		if err := cmd.Run(); err != nil {
			state.Status = types.StatusWarn
			state.Message = fmt.Sprintf("포트 %d 연결됨, pg_isready 실패", port)
			state.ResponseTime = int(time.Since(start).Milliseconds())
			return state
		}
	}
	state.Status = types.StatusUp
	state.Message = fmt.Sprintf("포트 %d 정상", port)
	state.ResponseTime = int(time.Since(start).Milliseconds())
	return state
}

func (c *Checker) getPostgreSQLPortAndPath() (int, string) {
	patterns := []string{"/etc/postgresql/*/main/postgresql.conf", "/var/lib/pgsql/data/postgresql.conf"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if port := c.parseConfigPort(path, "port"); port > 0 {
				return port, path
			}
			if _, err := os.Stat(path); err == nil {
				if c.isPortListening(5432) {
					return 5432, path
				}
			}
		}
	}
	if c.isPortListening(5432) {
		return 5432, ""
	}
	return 0, ""
}

func (c *Checker) CheckRedis() *types.ServiceState {
	port, configPath := c.getRedisPortAndPath()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-redis", Name: "Redis (OS)", Type: types.TypeRedis,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("redis-server"),
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	conn.SetDeadline(time.Now().Add(c.timeout))

	// RESP 프로토콜로 PING 전송
	conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	conn.Close()
	elapsed := int(time.Since(start).Milliseconds())
	response := string(buf[:n])

	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("Redis 응답 실패: %v", err)
		state.ResponseTime = elapsed
		return state
	}

	// PONG 응답 확인
	if strings.Contains(response, "PONG") {
		state.Status = types.StatusUp
		state.Message = fmt.Sprintf("포트 %d PONG 응답 정상", port)
		state.ResponseTime = elapsed
		return state
	}

	// 인증 필요한 경우
	if strings.Contains(response, "NOAUTH") || strings.Contains(response, "AUTH") {
		state.Status = types.StatusUp // 서버는 정상 동작 중
		state.Message = fmt.Sprintf("포트 %d 정상 (인증 필요)", port)
		state.ResponseTime = elapsed
		return state
	}

	// 기타 응답
	state.Status = types.StatusWarn
	state.Message = fmt.Sprintf("포트 %d 연결됨, 응답: %s", port, strings.TrimSpace(response))
	state.ResponseTime = elapsed
	return state
}

func (c *Checker) getRedisPortAndPath() (int, string) {
	paths := []string{"/etc/redis/redis.conf", "/etc/redis.conf"}
	for _, p := range paths {
		if port := c.parseConfigPort(p, "port"); port > 0 {
			return port, p
		}
		if _, err := os.Stat(p); err == nil {
			if c.isPortListening(6379) {
				return 6379, p
			}
		}
	}
	if c.isPortListening(6379) {
		return 6379, ""
	}
	return 0, ""
}

func (c *Checker) CheckMongoDB() *types.ServiceState {
	port, configPath := c.getMongoDBPortAndPath()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-mongodb", Name: "MongoDB (OS)", Type: types.TypeMongoDB,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("mongod"),
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	if err != nil {
		state.Status = types.StatusDown
		state.Message = fmt.Sprintf("연결 실패: %v", err)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	conn.Close()
	state.Status = types.StatusUp
	state.Message = fmt.Sprintf("포트 %d 정상", port)
	state.ResponseTime = int(time.Since(start).Milliseconds())
	return state
}

func (c *Checker) getMongoDBPortAndPath() (int, string) {
	paths := []string{"/etc/mongod.conf", "/etc/mongodb.conf"}
	for _, p := range paths {
		if port := c.parseYAMLPort(p, "port"); port > 0 {
			return port, p
		}
		if _, err := os.Stat(p); err == nil {
			if c.isPortListening(27017) {
				return 27017, p
			}
		}
	}
	if c.isPortListening(27017) {
		return 27017, ""
	}
	return 0, ""
}

func (c *Checker) parseConfigPort(path, key string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	re := regexp.MustCompile(fmt.Sprintf(`^\s*%s\s*[=\s]\s*(\d+)`, key))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			port, _ := strconv.Atoi(matches[1])
			return port
		}
	}
	return 0
}

func (c *Checker) parseYAMLPort(path, key string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	re := regexp.MustCompile(fmt.Sprintf(`^\s*%s:\s*(\d+)`, key))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if matches := re.FindStringSubmatch(scanner.Text()); len(matches) > 1 {
			port, _ := strconv.Atoi(matches[1])
			return port
		}
	}
	return 0
}

func (c *Checker) isPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Checker) commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func (c *Checker) findExecutable(names ...string) string {
	// 실행 파일 검색 경로
	searchPaths := []string{
		"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin",
		"/bin", "/sbin", "/opt/mysql/bin", "/opt/postgresql/bin",
	}
	for _, name := range names {
		// PATH에서 찾기
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		// 직접 경로 검색
		for _, dir := range searchPaths {
			fullPath := filepath.Join(dir, name)
			if _, err := os.Stat(fullPath); err == nil {
				return fullPath
			}
		}
	}
	return ""
}

// isSystemctlActive systemctl로 서비스 활성 상태 확인
func (c *Checker) isSystemctlActive(serviceName string) bool {
	// 먼저 systemctl is-active 시도
	cmd := exec.Command("systemctl", "is-active", serviceName)
	output, err := cmd.Output()
	if err == nil {
		status := strings.TrimSpace(string(output))
		log.Printf("[DEBUG] systemctl is-active %s: %s", serviceName, status)
		return status == "active"
	}

	// 실패시 systemctl status로 확인
	cmd = exec.Command("systemctl", "status", serviceName)
	output, _ = cmd.CombinedOutput()
	outputStr := string(output)
	isActive := strings.Contains(outputStr, "Active: active")
	log.Printf("[DEBUG] systemctl status %s: active=%v", serviceName, isActive)
	return isActive
}

// getSystemctlServiceNames 서비스에 해당하는 systemctl 서비스명 목록 반환
func (c *Checker) getSystemctlServiceNames(serviceType string) []string {
	switch serviceType {
	case "nginx":
		return []string{"nginx"}
	case "httpd":
		return []string{"httpd", "apache2"}
	default:
		return []string{serviceType}
	}
}

// CheckNginx Nginx 웹 서버 체크
func (c *Checker) CheckNginx() *types.ServiceState {
	// systemctl로 서비스 상태 먼저 확인
	isActive := c.isSystemctlActive("nginx")
	port, configPath := c.getNginxPortAndPath()
	execPath := c.findExecutable("nginx")

	log.Printf("[DEBUG] Nginx check: isActive=%v, port=%d, config=%s, exec=%s", isActive, port, configPath, execPath)

	// 서비스가 활성화되지 않았고 포트도 없으면 설치되지 않은 것으로 간주
	if !isActive && port == 0 {
		// nginx 실행 파일도 없으면 nil
		if execPath == "" {
			log.Printf("[DEBUG] Nginx not found (no systemctl, no port, no executable)")
			return nil
		}
	}

	// 포트가 0이면 기본 포트 사용
	if port == 0 {
		port = 80
	}

	state := &types.ServiceState{
		ID: "os-nginx", Name: "Nginx (OS)", Type: types.TypeWebNginx,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       execPath,
	}
	start := time.Now()

	// systemctl에서 비활성 상태면 DOWN
	if !isActive {
		state.Status = types.StatusDown
		state.Message = "서비스 비활성 (systemctl)"
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}

	// HTTP 요청으로 응답 확인
	status, msg, elapsed := c.httpCheck(fmt.Sprintf("http://localhost:%d/", port))
	state.Status = status
	state.Message = msg
	state.ResponseTime = elapsed
	if elapsed == 0 {
		state.ResponseTime = int(time.Since(start).Milliseconds())
	}
	return state
}

func (c *Checker) getNginxPortAndPath() (int, string) {
	paths := []string{"/etc/nginx/nginx.conf", "/usr/local/nginx/conf/nginx.conf"}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			// nginx.conf에서 listen 포트 파싱
			if port := c.parseNginxListenPort(p); port > 0 {
				return port, p
			}
			// 기본 포트 확인
			if c.isPortListening(80) {
				return 80, p
			}
			if c.isPortListening(443) {
				return 443, p
			}
		}
	}
	// 설정 파일 없이 포트만 확인
	if c.isPortListening(80) && c.findExecutable("nginx") != "" {
		return 80, ""
	}
	return 0, ""
}

func (c *Checker) parseNginxListenPort(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	re := regexp.MustCompile(`listen\s+(\d+)`)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			port, _ := strconv.Atoi(matches[1])
			if port > 0 {
				return port
			}
		}
	}
	return 0
}

// CheckHTTPD Apache HTTPD 웹 서버 체크
func (c *Checker) CheckHTTPD() *types.ServiceState {
	// systemctl로 서비스 상태 먼저 확인 (httpd 또는 apache2)
	isActiveHttpd := c.isSystemctlActive("httpd")
	isActiveApache2 := c.isSystemctlActive("apache2")
	isActive := isActiveHttpd || isActiveApache2
	port, configPath := c.getHTTPDPortAndPath()
	execPath := c.findExecutable("httpd", "apache2")

	log.Printf("[DEBUG] HTTPD check: isActive(httpd=%v,apache2=%v), port=%d, config=%s, exec=%s",
		isActiveHttpd, isActiveApache2, port, configPath, execPath)

	// 서비스가 활성화되지 않았고 포트도 없으면 설치되지 않은 것으로 간주
	if !isActive && port == 0 {
		// httpd/apache2 실행 파일도 없으면 nil
		if execPath == "" {
			log.Printf("[DEBUG] HTTPD not found (no systemctl, no port, no executable)")
			return nil
		}
	}

	// 포트가 0이면 기본 포트 사용
	if port == 0 {
		port = 80
	}

	state := &types.ServiceState{
		ID: "os-httpd", Name: "Apache HTTPD (OS)", Type: types.TypeWebApache,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
		ConfigPath: configPath,
		Path:       execPath,
	}
	start := time.Now()

	// systemctl에서 비활성 상태면 DOWN
	if !isActive {
		state.Status = types.StatusDown
		state.Message = "서비스 비활성 (systemctl)"
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}

	// HTTP 요청으로 응답 확인
	status, msg, elapsed := c.httpCheck(fmt.Sprintf("http://localhost:%d/", port))
	state.Status = status
	state.Message = msg
	state.ResponseTime = elapsed
	if elapsed == 0 {
		state.ResponseTime = int(time.Since(start).Milliseconds())
	}
	return state
}

func (c *Checker) getHTTPDPortAndPath() (int, string) {
	paths := []string{
		"/etc/httpd/conf/httpd.conf",           // CentOS/RHEL
		"/etc/apache2/apache2.conf",            // Debian/Ubuntu
		"/etc/apache2/ports.conf",              // Debian/Ubuntu ports
		"/usr/local/apache2/conf/httpd.conf",   // Manual install
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			if port := c.parseHTTPDListenPort(p); port > 0 {
				return port, p
			}
			if c.isPortListening(80) {
				return 80, p
			}
		}
	}
	// 설정 파일 없이 포트만 확인
	if c.isPortListening(80) && c.findExecutable("httpd", "apache2") != "" {
		return 80, ""
	}
	return 0, ""
}

func (c *Checker) parseHTTPDListenPort(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	re := regexp.MustCompile(`(?i)^Listen\s+(\d+)`)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			port, _ := strconv.Atoi(matches[1])
			if port > 0 {
				return port
			}
		}
	}
	return 0
}

// httpCheck HTTP 요청으로 상태 확인
func (c *Checker) httpCheck(url string) (types.Status, string, int) {
	start := time.Now()
	client := &net.Dialer{Timeout: c.timeout}
	conn, err := client.Dial("tcp", strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://"))
	if err != nil {
		return types.StatusDown, fmt.Sprintf("연결 실패: %v", err), int(time.Since(start).Milliseconds())
	}
	conn.Close()

	// HTTP GET 요청
	resp, err := (&http.Client{Timeout: c.timeout}).Get(url)
	elapsed := int(time.Since(start).Milliseconds())
	if err != nil {
		return types.StatusDown, fmt.Sprintf("HTTP 요청 실패: %v", err), elapsed
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return types.StatusUp, fmt.Sprintf("%d OK", resp.StatusCode), elapsed
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return types.StatusWarn, fmt.Sprintf("%d %s", resp.StatusCode, resp.Status), elapsed
	}
	if resp.StatusCode >= 500 {
		return types.StatusDown, fmt.Sprintf("%d %s", resp.StatusCode, resp.Status), elapsed
	}
	return types.StatusUp, fmt.Sprintf("%d %s", resp.StatusCode, resp.Status), elapsed
}
