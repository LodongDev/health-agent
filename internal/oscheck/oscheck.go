package oscheck

import (
	"bufio"
	"fmt"
	"net"
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
