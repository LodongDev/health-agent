package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"health-agent/internal/auth"
	"health-agent/internal/config"
	"health-agent/internal/docker"
	"health-agent/internal/oscheck"
	"health-agent/internal/types"
	"health-agent/internal/wsclient"

	"golang.org/x/term"
)

const version = "1.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "login":
		cmdLogin()
	case "logout":
		cmdLogout()
	case "whoami":
		cmdWhoami()
	case "docker":
		cmdDocker()
	case "lxd":
		cmdLxd()
	case "version", "-v", "--version":
		fmt.Printf("Health Agent v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "알 수 없는 명령: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Health Agent - 서비스 헬스체크 에이전트")
	fmt.Println()
	fmt.Println("사용법:")
	fmt.Println("  health-agent <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  login     로그인")
	fmt.Println("  logout    로그아웃")
	fmt.Println("  whoami    현재 로그인 상태")
	fmt.Println()
	fmt.Println("  docker    Docker 컨테이너 + OS 서비스 모니터링")
	fmt.Println("  lxd       LXD 컨테이너 + OS 서비스 모니터링 (예정)")
	fmt.Println()
	fmt.Println("  version   버전 정보")
	fmt.Println("  help      도움말")
	fmt.Println()
	fmt.Println("예시:")
	fmt.Println("  health-agent login")
	fmt.Println("  health-agent docker")
	fmt.Println("  health-agent docker --once")
}

func cmdLogin() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		password, _ := reader.ReadString('\n')
		passwordBytes = []byte(strings.TrimSpace(password))
	}
	fmt.Println()

	password := string(passwordBytes)

	if email == "" || password == "" {
		fmt.Fprintln(os.Stderr, "이메일과 비밀번호를 입력하세요")
		os.Exit(1)
	}

	authClient := auth.NewClient(config.AuthURL)
	token, err := authClient.Login(email, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "로그인 실패: %v\n", err)
		os.Exit(1)
	}

	if err := auth.SaveToken(token); err != nil {
		fmt.Fprintf(os.Stderr, "토큰 저장 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[INFO] 로그인 성공 (%s)\n", email)
}

func cmdLogout() {
	if !auth.TokenExists() {
		fmt.Println("이미 로그아웃 상태입니다")
		return
	}

	if err := auth.DeleteToken(); err != nil {
		fmt.Fprintf(os.Stderr, "로그아웃 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[INFO] 로그아웃 완료")
}

func cmdWhoami() {
	token, err := auth.EnsureValidToken(config.AuthURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	authClient := auth.NewClient(config.AuthURL)
	user, err := authClient.GetMe(token.AccessToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "사용자 정보 조회 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("로그인: %s (%s)\n", user.Email, user.Name)
	fmt.Printf("부서: %s\n", user.Department)
	fmt.Printf("토큰 만료: %s\n", token.ExpiresAt.Format("2006-01-02 15:04:05"))
}

func cmdDocker() {
	// 1. 인증 확인
	token, err := auth.EnsureValidToken(config.AuthURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		fmt.Fprintln(os.Stderr, "먼저 'health-agent login' 명령으로 로그인하세요")
		os.Exit(1)
	}
	fmt.Printf("[INFO] 인증 확인 (%s)\n", token.Email)

	// 2. 옵션 파싱
	once := false
	for _, arg := range os.Args[2:] {
		if arg == "--once" {
			once = true
		}
	}

	// 3. 에이전트 실행
	agent := NewAgent(token)
	agent.Run(once)
}

func cmdLxd() {
	fmt.Println("[INFO] LXD 모니터링은 아직 구현되지 않았습니다.")
	os.Exit(1)
}

type Agent struct {
	token       *auth.TokenData
	wsClient    *wsclient.Client
	osChecker   *oscheck.Checker
	dockerCheck *docker.Checker
	hostname    string
	agentID     string
	states      map[string]*types.ServiceState
}

func NewAgent(token *auth.TokenData) *Agent {
	hostname, _ := os.Hostname()
	agentID := config.LoadOrCreateAgentID()

	return &Agent{
		token:       token,
		osChecker:   oscheck.New(),
		dockerCheck: docker.New(),
		hostname:    hostname,
		agentID:     agentID,
		states:      make(map[string]*types.ServiceState),
	}
}

func (a *Agent) Run(once bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 시그널 핸들링
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	a.printBanner()

	// WebSocket 연결
	var err error
	a.wsClient, err = wsclient.New(config.WebSocketURL, a.token.AccessToken)
	if err != nil {
		log.Fatalf("[ERROR] WebSocket 연결 실패: %v", err)
	}
	defer a.wsClient.Close()
	log.Println("[INFO] 서버 연결 완료")

	// Docker 연결 확인
	if err := a.dockerCheck.Ping(ctx); err != nil {
		log.Printf("[WARN] Docker 연결 실패: %v (Docker 체크 건너뜀)", err)
	} else {
		log.Println("[INFO] Docker 연결 확인")
	}

	// 한 번만 실행 모드
	if once {
		a.runOnce(ctx)
		return
	}

	// 메인 루프
	checkTicker := time.NewTicker(30 * time.Second)
	defer checkTicker.Stop()

	log.Println("[INFO] 모니터링 시작 (30초 간격)")

	// 즉시 첫 체크
	a.check(ctx)

	for {
		select {
		case <-checkTicker.C:
			a.check(ctx)
		case <-sigCh:
			log.Println("\n[INFO] 종료 중...")
			return
		}
	}
}

func (a *Agent) runOnce(ctx context.Context) {
	a.check(ctx)
	a.printSummary()
}

func (a *Agent) check(ctx context.Context) {
	start := time.Now()
	var results []types.ServiceState

	// 1. OS 서비스 체크 (항상 실행)
	log.Println("[INFO] OS 서비스 체크 중...")
	osResults := a.osChecker.CheckAll()
	for _, r := range osResults {
		results = append(results, r)
		a.handleStateChange(r)
	}

	// 2. Docker 컨테이너 체크
	log.Println("[INFO] Docker 컨테이너 체크 중...")
	dockerResults, err := a.dockerCheck.CheckAll(ctx)
	if err != nil {
		log.Printf("[WARN] Docker 체크 실패: %v", err)
	} else {
		for _, r := range dockerResults {
			results = append(results, r)
			a.handleStateChange(r)
		}
	}

	// 3. 서버로 전송
	if err := a.sendResults(results); err != nil {
		log.Printf("[ERROR] 결과 전송 실패: %v", err)
	}

	log.Printf("[INFO] 체크 완료: %d개 서비스, %v", len(results), time.Since(start).Round(time.Millisecond))
}

func (a *Agent) handleStateChange(current types.ServiceState) {
	prev, exists := a.states[current.ID]

	// 상태 저장
	a.states[current.ID] = &current

	if !exists {
		return
	}

	// 상태 변경 감지
	if prev.Status != current.Status {
		log.Printf("[ALERT] %s: %s -> %s", current.Name, prev.Status, current.Status)
	}
}

func (a *Agent) sendResults(results []types.ServiceState) error {
	payload := types.AgentReport{
		AgentID:   a.agentID,
		Hostname:  a.hostname,
		Timestamp: time.Now(),
		Services:  results,
	}
	return a.wsClient.SendReport(payload)
}

func (a *Agent) printBanner() {
	fmt.Println("==========================================")
	fmt.Printf(" Health Agent v%s\n", version)
	fmt.Printf(" Agent ID : %s\n", a.agentID)
	fmt.Printf(" Hostname : %s\n", a.hostname)
	fmt.Printf(" Server   : %s\n", config.MonitoringAPIURL)
	fmt.Println("==========================================")
}

func (a *Agent) printSummary() {
	fmt.Println("\n요약:")
	fmt.Println("------------------------------------------")

	up, down, warn := 0, 0, 0
	for _, state := range a.states {
		switch state.Status {
		case types.StatusUp:
			up++
		case types.StatusDown:
			down++
		case types.StatusWarn:
			warn++
		}

		statusMark := "[UP]"
		if state.Status == types.StatusDown {
			statusMark = "[DOWN]"
		} else if state.Status == types.StatusWarn {
			statusMark = "[WARN]"
		}

		fmt.Printf("%s %-25s %s %s\n", statusMark, state.Name, state.Type, state.Message)
	}

	fmt.Println("------------------------------------------")
	fmt.Printf("총 %d개 | UP: %d | DOWN: %d | WARN: %d\n", len(a.states), up, down, warn)
}
