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

	"docker-health-agent/internal/auth"
	"docker-health-agent/internal/checker"
	"docker-health-agent/internal/client"
	"docker-health-agent/internal/config"
	"docker-health-agent/internal/discovery"
	"docker-health-agent/internal/resolver"
	"docker-health-agent/internal/types"

	"golang.org/x/term"
)

const version = "1.0.0"

type Agent struct {
	cfg       *config.Config
	discovery *discovery.Discovery
	resolver  *resolver.Resolver
	checker   *checker.Checker
	client    *client.Client
	states    map[string]*types.ContainerState
}

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
	case "run":
		cmdRun()
	case "version", "-v", "--version":
		fmt.Printf("Docker Health Agent v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "알 수 없는 명령: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Docker Health Agent - 컨테이너 헬스체크 에이전트")
	fmt.Println()
	fmt.Println("사용법:")
	fmt.Println("  docker-health-agent <command> [옵션]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  login     로그인 (이메일/비밀번호)")
	fmt.Println("  logout    로그아웃")
	fmt.Println("  whoami    현재 로그인 상태 확인")
	fmt.Println("  run       에이전트 실행 (로그인 필수)")
	fmt.Println("  version   버전 정보")
	fmt.Println("  help      도움말")
	fmt.Println()
	fmt.Println("예시:")
	fmt.Println("  docker-health-agent login")
	fmt.Println("  docker-health-agent run --api-url http://172.27.1.1:11401/api/gomtang-alert")
	fmt.Println()
	fmt.Println("run 명령의 상세 옵션:")
	fmt.Println("  docker-health-agent run --help")
}

// cmdLogin 로그인 명령 처리
func cmdLogin() {
	authURL := config.GetAuthURL()

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		// 터미널이 아닌 경우 일반 입력으로 폴백
		password, _ := reader.ReadString('\n')
		passwordBytes = []byte(strings.TrimSpace(password))
	}
	fmt.Println()

	password := string(passwordBytes)

	if email == "" || password == "" {
		fmt.Fprintln(os.Stderr, "이메일과 비밀번호를 입력하세요")
		os.Exit(1)
	}

	authClient := auth.NewClient(authURL)
	token, err := authClient.Login(email, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "로그인 실패: %v\n", err)
		os.Exit(1)
	}

	if err := auth.SaveToken(token); err != nil {
		fmt.Fprintf(os.Stderr, "토큰 저장 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("로그인 성공 (%s)\n", email)
}

// cmdLogout 로그아웃 명령 처리
func cmdLogout() {
	if !auth.TokenExists() {
		fmt.Println("이미 로그아웃 상태입니다")
		return
	}

	if err := auth.DeleteToken(); err != nil {
		fmt.Fprintf(os.Stderr, "로그아웃 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("로그아웃 완료")
}

// cmdWhoami 현재 로그인 상태 확인
func cmdWhoami() {
	authURL := config.GetAuthURL()

	token, err := auth.EnsureValidToken(authURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	authClient := auth.NewClient(authURL)
	user, err := authClient.GetMe(token.AccessToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "사용자 정보 조회 실패: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("로그인: %s (%s)\n", user.Email, user.Name)
	fmt.Printf("역할: %s\n", user.Role)
	fmt.Printf("토큰 만료: %s\n", token.ExpiresAt.Format("2006-01-02 15:04:05"))
}

// cmdRun 에이전트 실행 명령 처리
func cmdRun() {
	// 1. 인증 확인
	authURL := config.GetAuthURL()
	token, err := auth.EnsureValidToken(authURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fmt.Fprintln(os.Stderr, "먼저 'docker-health-agent login' 명령으로 로그인하세요")
		os.Exit(1)
	}

	fmt.Printf("인증 확인 (%s)\n", token.Email)

	// 2. 설정 파싱
	cfg, err := config.ParseRunFlags(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// 3. 에이전트 실행
	agent, err := NewAgent(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	agent.Run()
}

func NewAgent(cfg *config.Config) (*Agent, error) {
	disc, err := discovery.New(cfg.DockerSock, cfg.LabelPrefix)
	if err != nil {
		return nil, fmt.Errorf("Docker 연결 실패: %w", err)
	}

	return &Agent{
		cfg:       cfg,
		discovery: disc,
		resolver:  resolver.New(cfg.LabelPrefix),
		checker:   checker.New(cfg.Timeout, cfg.LabelPrefix),
		client:    client.New(cfg.APIURL, cfg.APIToken),
		states:    make(map[string]*types.ContainerState),
	}, nil
}

func (a *Agent) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 시그널 핸들링
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	a.printBanner()

	// Docker 연결 확인
	if err := a.discovery.Ping(ctx); err != nil {
		log.Fatalf("Docker 연결 실패: %v", err)
	}
	log.Println("Docker 연결 확인")

	// 에이전트 등록
	agentInfo := types.AgentInfo{
		AgentID:   a.cfg.AgentID,
		Hostname:  a.cfg.Hostname,
		IP:        config.GetLocalIP(),
		Version:   version,
		StartedAt: time.Now(),
	}

	if err := a.client.RegisterAgent(ctx, agentInfo); err != nil {
		log.Printf("에이전트 등록 실패 (오프라인 모드): %v", err)
	} else {
		log.Println("에이전트 등록 완료")
	}

	// 한 번만 실행 모드
	if a.cfg.Once {
		a.runOnce(ctx)
		return
	}

	// 메인 루프
	checkTicker := time.NewTicker(a.cfg.CheckInterval)
	reportTicker := time.NewTicker(a.cfg.ReportInterval)
	defer checkTicker.Stop()
	defer reportTicker.Stop()

	log.Printf("에이전트 시작 (체크: %v, 보고: %v)", a.cfg.CheckInterval, a.cfg.ReportInterval)

	// 즉시 첫 체크
	a.check(ctx)

	for {
		select {
		case <-checkTicker.C:
			a.check(ctx)
		case <-reportTicker.C:
			a.report(ctx)
		case <-sigCh:
			log.Println("\n종료 중...")
			return
		}
	}
}

func (a *Agent) runOnce(ctx context.Context) {
	a.check(ctx)
	a.report(ctx)
	a.printSummary()
}

func (a *Agent) check(ctx context.Context) {
	start := time.Now()

	containers, err := a.discovery.Discover(ctx)
	if err != nil {
		log.Printf("컨테이너 조회 실패: %v", err)
		return
	}

	activeIDs := make(map[string]bool)

	for _, container := range containers {
		activeIDs[container.ID] = true

		ctype := a.resolver.Resolve(container)
		health := a.checker.Check(ctx, container, ctype)

		// 이전 상태와 비교
		prevState := a.states[container.ID]
		statusChanged := prevState != nil && prevState.Health.Status != health.Status

		// 상태 저장
		a.states[container.ID] = &types.ContainerState{
			Container: container,
			Type:      ctype,
			Health:    health,
		}

		// 상태 변경 시 알림
		if statusChanged {
			a.sendAlert(ctx, container, ctype, prevState.Health.Status, health)
		}

		if a.cfg.LogLevel == "debug" {
			log.Printf("[%s] %s(%s) -> %s", container.Name, ctype.Type, ctype.Subtype, health.Status)
		}
	}

	// 사라진 컨테이너 처리
	for id, state := range a.states {
		if !activeIDs[id] {
			a.sendAlert(ctx, state.Container, state.Type, state.Health.Status, types.HealthResult{
				Status:    types.StatusDown,
				Message:   "컨테이너 사라짐",
				CheckedAt: time.Now(),
			})
			delete(a.states, id)
		}
	}

	log.Printf("체크 완료: %d개, %v", len(containers), time.Since(start).Round(time.Millisecond))
}

func (a *Agent) report(ctx context.Context) {
	var containers []types.ContainerReport
	stats := types.ReportStats{}

	for _, state := range a.states {
		containers = append(containers, types.ContainerReport{
			ID:     state.Container.ID[:12],
			Name:   state.Container.Name,
			Image:  state.Container.Image,
			Type:   state.Type,
			Health: state.Health,
			Ports:  state.Container.Ports,
		})

		stats.Total++
		switch state.Health.Status {
		case types.StatusUp:
			stats.Up++
		case types.StatusDown:
			stats.Down++
		case types.StatusDegraded:
			stats.Degraded++
		}
	}

	payload := types.ReportPayload{
		AgentID:    a.cfg.AgentID,
		Hostname:   a.cfg.Hostname,
		Timestamp:  time.Now().Format(time.RFC3339),
		Containers: containers,
		Stats:      stats,
	}

	if err := a.client.ReportContainers(ctx, payload); err != nil {
		log.Printf("보고 실패: %v", err)
	} else if a.cfg.LogLevel == "debug" {
		log.Printf("보고: %d개 컨테이너", len(containers))
	}
}

func (a *Agent) sendAlert(ctx context.Context, container types.ContainerInfo, ctype types.ContainerType, prev types.HealthStatus, health types.HealthResult) {
	payload := types.AlertPayload{
		AgentID:   a.cfg.AgentID,
		Hostname:  a.cfg.Hostname,
		Timestamp: time.Now().Format(time.RFC3339),
		Container: types.AlertContainer{
			ID:    container.ID[:12],
			Name:  container.Name,
			Image: container.Image,
			Type:  ctype.Type,
		},
		PreviousStatus: prev,
		CurrentStatus:  health.Status,
		Message:        health.Message,
	}

	// DOWN일 때만 로그 + 알림
	if health.Status == types.StatusDown {
		log.Printf("%s: %s -> DOWN (%s)", container.Name, prev, health.Message)
		if err := a.client.SendAlert(ctx, payload); err != nil {
			log.Printf("알림 전송 실패: %v", err)
		}
	} else if prev == types.StatusDown && health.Status == types.StatusUp {
		log.Printf("%s: DOWN -> UP (복구)", container.Name)
		if err := a.client.SendAlert(ctx, payload); err != nil {
			log.Printf("복구 알림 실패: %v", err)
		}
	}
}

func (a *Agent) printBanner() {
	fmt.Println("==========================================")
	fmt.Printf(" Docker Health Agent v%s\n", version)
	fmt.Printf(" Agent ID : %s\n", a.cfg.AgentID)
	fmt.Printf(" Hostname : %s\n", a.cfg.Hostname)
	fmt.Printf(" API URL  : %s\n", a.cfg.APIURL)
	fmt.Println("==========================================")
}

func (a *Agent) printSummary() {
	fmt.Println("\n요약:")
	fmt.Println("------------------------------------------")

	up, down, degraded := 0, 0, 0
	for _, state := range a.states {
		switch state.Health.Status {
		case types.StatusUp:
			up++
		case types.StatusDown:
			down++
		case types.StatusDegraded:
			degraded++
		}

		statusMark := "[UP]"
		if state.Health.Status == types.StatusDown {
			statusMark = "[DOWN]"
		} else if state.Health.Status == types.StatusDegraded {
			statusMark = "[WARN]"
		}

		fmt.Printf("%s %-20s %s(%s) %s\n",
			statusMark,
			state.Container.Name,
			state.Type.Type,
			state.Type.Subtype,
			state.Health.Message)
	}

	fmt.Println("------------------------------------------")
	fmt.Printf("총 %d개 | UP: %d | DOWN: %d | DEGRADED: %d\n",
		len(a.states), up, down, degraded)
}
