package oscheck

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
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
	timeout    time.Duration
	httpClient *http.Client // 공유 HTTP 클라이언트 (연결 재사용)
}

func New() *Checker {
	// 공유 HTTP 클라이언트 생성 (연결 풀링)
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 5,
			MaxConnsPerHost:     10,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
		},
	}

	return &Checker{
		timeout:    5 * time.Second,
		httpClient: httpClient,
	}
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
		ID:         "os-mysql",
		Name:       "MySQL (OS)",
		Type:       types.TypeMySQL,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("mysqld", "mysql"),
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.ContainerState = "inactive"
		state.HttpCheck = &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
		return state
	}
	conn.Close()

	state.ContainerState = "active"
	state.HttpCheck = &types.CheckResult{
		Success:      true,
		StatusCode:   200, // TCP 연결 성공
		ResponseTime: elapsed,
	}
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
		ID:         "os-postgresql",
		Name:       "PostgreSQL (OS)",
		Type:       types.TypePostgreSQL,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("postgres", "postgresql"),
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.ContainerState = "inactive"
		state.HttpCheck = &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
		return state
	}
	conn.Close()

	state.ContainerState = "active"
	state.HttpCheck = &types.CheckResult{
		Success:      true,
		StatusCode:   200,
		ResponseTime: elapsed,
	}
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
		ID:         "os-redis",
		Name:       "Redis (OS)",
		Type:       types.TypeRedis,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("redis-server"),
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	if err != nil {
		elapsed := int(time.Since(start).Milliseconds())
		state.ContainerState = "inactive"
		state.HttpCheck = &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
		return state
	}
	conn.SetDeadline(time.Now().Add(c.timeout))

	// RESP 프로토콜로 PING 전송
	conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	buf := make([]byte, 128)
	_, _ = conn.Read(buf)
	conn.Close()
	elapsed := int(time.Since(start).Milliseconds())

	state.ContainerState = "active"
	state.HttpCheck = &types.CheckResult{
		Success:      true,
		StatusCode:   200, // Redis 연결 성공
		ResponseTime: elapsed,
	}
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
		ID:         "os-mongodb",
		Name:       "MongoDB (OS)",
		Type:       types.TypeMongoDB,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       c.findExecutable("mongod"),
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), c.timeout)
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		state.ContainerState = "inactive"
		state.HttpCheck = &types.CheckResult{
			Success:      false,
			StatusCode:   0,
			ResponseTime: elapsed,
			Error:        err.Error(),
		}
		return state
	}
	conn.Close()

	state.ContainerState = "active"
	state.HttpCheck = &types.CheckResult{
		Success:      true,
		StatusCode:   200,
		ResponseTime: elapsed,
	}
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
	if !isActive && port == 0 && execPath == "" {
		return nil
	}

	// 포트가 0이면 기본 포트 사용
	if port == 0 {
		port = 80
	}

	state := &types.ServiceState{
		ID:         "os-nginx",
		Name:       "Nginx (OS)",
		Type:       types.TypeWebNginx,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       execPath,
	}

	// systemctl 기반 상태
	if isActive {
		state.ContainerState = "active"
	} else {
		state.ContainerState = "inactive"
	}

	// HTTP 체크
	state.HttpCheck = c.doHTTPCheck(fmt.Sprintf("http://localhost:%d/", port))
	return state
}

// doHTTPCheck HTTP 요청으로 raw 데이터 수집 (공유 클라이언트 사용)
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
	// 연결 재사용을 위해 응답 본문을 완전히 drain
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	return &types.CheckResult{
		Success:      true,
		StatusCode:   resp.StatusCode,
		ResponseTime: elapsed,
	}
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
	if !isActive && port == 0 && execPath == "" {
		return nil
	}

	// 포트가 0이면 기본 포트 사용
	if port == 0 {
		port = 80
	}

	state := &types.ServiceState{
		ID:         "os-httpd",
		Name:       "Apache HTTPD (OS)",
		Type:       types.TypeWebApache,
		Host:       "localhost",
		Port:       port,
		CheckedAt:  time.Now(),
		ConfigPath: configPath,
		Path:       execPath,
	}

	// systemctl 기반 상태
	if isActive {
		state.ContainerState = "active"
	} else {
		state.ContainerState = "inactive"
	}

	// HTTP 체크
	state.HttpCheck = c.doHTTPCheck(fmt.Sprintf("http://localhost:%d/", port))
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

