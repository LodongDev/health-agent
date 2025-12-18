package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/google/uuid"
)

// DefaultAuthURL 기본 인증 서버 URL
const DefaultAuthURL = "http://172.27.50.118:10709"

// Config 에이전트 설정
type Config struct {
	// 인증 설정
	AuthURL string

	// API 설정
	APIURL   string
	APIToken string

	// Docker 설정
	DockerSock  string
	LabelPrefix string

	// 실행 설정
	CheckInterval  time.Duration
	ReportInterval time.Duration
	Timeout        time.Duration

	// 에이전트 정보
	AgentID  string
	Hostname string

	// 기타
	LogLevel string
	Once     bool // 한 번만 실행
}

// ParseRunFlags run 서브커맨드의 플래그 파싱
func ParseRunFlags(args []string) (*Config, error) {
	cfg := &Config{}

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.StringVar(&cfg.AuthURL, "auth-url", getEnv("AUTH_URL", DefaultAuthURL), "인증 서버 URL")
	fs.StringVar(&cfg.APIURL, "api-url", getEnv("API_URL", ""), "중앙 서버 API URL (필수)")
	fs.StringVar(&cfg.APIToken, "api-token", getEnv("API_TOKEN", ""), "API 인증 토큰")
	fs.StringVar(&cfg.DockerSock, "docker-sock", getEnv("DOCKER_SOCK", "/var/run/docker.sock"), "Docker 소켓 경로")
	fs.StringVar(&cfg.LabelPrefix, "label-prefix", getEnv("LABEL_PREFIX", "monitor"), "Label prefix")
	fs.DurationVar(&cfg.CheckInterval, "interval", getDurationEnv("CHECK_INTERVAL", 30*time.Second), "체크 주기")
	fs.DurationVar(&cfg.ReportInterval, "report-interval", getDurationEnv("REPORT_INTERVAL", 60*time.Second), "보고 주기")
	fs.DurationVar(&cfg.Timeout, "timeout", getDurationEnv("TIMEOUT", 5*time.Second), "헬스체크 타임아웃")
	fs.StringVar(&cfg.AgentID, "agent-id", getEnv("AGENT_ID", ""), "에이전트 ID (자동 생성)")
	fs.StringVar(&cfg.Hostname, "hostname", "", "호스트명 (자동 감지)")
	fs.StringVar(&cfg.LogLevel, "log-level", getEnv("LOG_LEVEL", "info"), "로그 레벨 (debug/info/warn/error)")
	fs.BoolVar(&cfg.Once, "once", false, "한 번만 실행 후 종료")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "사용법: docker-health-agent run [옵션]\n\n")
		fmt.Fprintf(os.Stderr, "옵션:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\n예시:\n")
		fmt.Fprintf(os.Stderr, "  docker-health-agent run --api-url http://172.27.1.1:11401/api/gomtang-alert\n")
		fmt.Fprintf(os.Stderr, "  docker-health-agent run --api-url http://server/api --interval 60s --once\n")
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// 필수값 검증
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("--api-url 필수")
	}

	// 기본값 설정
	if cfg.AgentID == "" {
		cfg.AgentID = loadOrCreateAgentID()
	}
	if cfg.Hostname == "" {
		cfg.Hostname, _ = os.Hostname()
	}

	return cfg, nil
}

// GetAuthURL 환경변수 또는 기본값에서 AuthURL 가져오기
func GetAuthURL() string {
	return getEnv("AUTH_URL", DefaultAuthURL)
}

// GetLocalIP 로컬 IP 조회
func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func loadOrCreateAgentID() string {
	// /etc/docker-health-agent/agent-id 에서 읽기 시도
	idFile := "/etc/docker-health-agent/agent-id"
	if data, err := os.ReadFile(idFile); err == nil {
		return string(data)
	}

	// 새로 생성
	id := fmt.Sprintf("agent-%s", uuid.New().String()[:8])

	// 저장 시도 (실패해도 무시)
	os.MkdirAll("/etc/docker-health-agent", 0755)
	os.WriteFile(idFile, []byte(id), 0644)

	return id
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}
