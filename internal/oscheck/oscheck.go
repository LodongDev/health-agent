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
	port := c.getMySQLPort()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-mysql", Name: "MySQL (OS)", Type: types.TypeMySQL,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
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

func (c *Checker) getMySQLPort() int {
	paths := []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "/etc/mysql/mysql.conf.d/mysqld.cnf"}
	for _, p := range paths {
		if port := c.parseConfigPort(p, "port"); port > 0 {
			return port
		}
	}
	if c.isPortListening(3306) {
		return 3306
	}
	return 0
}

func (c *Checker) CheckPostgreSQL() *types.ServiceState {
	port := c.getPostgreSQLPort()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-postgresql", Name: "PostgreSQL (OS)", Type: types.TypePostgreSQL,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
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

func (c *Checker) getPostgreSQLPort() int {
	patterns := []string{"/etc/postgresql/*/main/postgresql.conf", "/var/lib/pgsql/data/postgresql.conf"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if port := c.parseConfigPort(path, "port"); port > 0 {
				return port
			}
		}
	}
	if c.isPortListening(5432) {
		return 5432
	}
	return 0
}

func (c *Checker) CheckRedis() *types.ServiceState {
	port := c.getRedisPort()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-redis", Name: "Redis (OS)", Type: types.TypeRedis,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
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
	conn.Write([]byte("PING\r\n"))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	conn.Close()
	if err != nil || !strings.Contains(string(buf[:n]), "PONG") {
		state.Status = types.StatusWarn
		state.Message = fmt.Sprintf("포트 %d 연결됨, PING 실패", port)
		state.ResponseTime = int(time.Since(start).Milliseconds())
		return state
	}
	state.Status = types.StatusUp
	state.Message = fmt.Sprintf("포트 %d 정상 (PONG)", port)
	state.ResponseTime = int(time.Since(start).Milliseconds())
	return state
}

func (c *Checker) getRedisPort() int {
	paths := []string{"/etc/redis/redis.conf", "/etc/redis.conf"}
	for _, p := range paths {
		if port := c.parseConfigPort(p, "port"); port > 0 {
			return port
		}
	}
	if c.isPortListening(6379) {
		return 6379
	}
	return 0
}

func (c *Checker) CheckMongoDB() *types.ServiceState {
	port := c.getMongoDBPort()
	if port == 0 {
		return nil
	}
	state := &types.ServiceState{
		ID: "os-mongodb", Name: "MongoDB (OS)", Type: types.TypeMongoDB,
		Host: "localhost", Port: port, CheckedAt: time.Now(),
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

func (c *Checker) getMongoDBPort() int {
	paths := []string{"/etc/mongod.conf", "/etc/mongodb.conf"}
	for _, p := range paths {
		if port := c.parseYAMLPort(p, "port"); port > 0 {
			return port
		}
	}
	if c.isPortListening(27017) {
		return 27017
	}
	return 0
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
