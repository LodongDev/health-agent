package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"health-agent/internal/config"
	"health-agent/internal/docker"
	"health-agent/internal/oscheck"
	"health-agent/internal/types"
	"health-agent/internal/wsclient"
)

const version = "1.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "config":
		cmdConfig()
	case "status":
		cmdStatus()
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
	fmt.Println("  config    API 키 설정")
	fmt.Println("            --api-key <key>  API 키 설정")
	fmt.Println("            --show           현재 설정 표시")
	fmt.Println()
	fmt.Println("  status    현재 설정 상태")
	fmt.Println()
	fmt.Println("  docker    Docker 컨테이너 + OS 서비스 모니터링")
	fmt.Println("  lxd       LXD 컨테이너 + OS 서비스 모니터링 (예정)")
	fmt.Println()
	fmt.Println("  version   버전 정보")
	fmt.Println("  help      도움말")
	fmt.Println()
	fmt.Println("예시:")
	fmt.Println("  health-agent config --api-key ldk_xxxxx")
	fmt.Println("  health-agent docker")
	fmt.Println("  health-agent docker --once")
}

func cmdConfig() {
	if len(os.Args) < 3 {
		// 현재 설정 표시
		cmdStatus()
		return
	}

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--api-key":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "API 키를 입력하세요")
				os.Exit(1)
			}
			apiKey := os.Args[i+1]
			if apiKey == "" || !strings.HasPrefix(apiKey, "ldk_") {
				fmt.Fprintln(os.Stderr, "올바른 API 키 형식이 아닙니다 (ldk_로 시작해야 함)")
				os.Exit(1)
			}

			cfg := &config.AgentConfig{APIKey: apiKey}
			if err := config.SaveConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "설정 저장 실패: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[INFO] API 키가 설정되었습니다\n")
			fmt.Printf("       키: %s****\n", apiKey[:12])
			return

		case "--show":
			cmdStatus()
			return
		}
	}
}

func cmdStatus() {
	if !config.ConfigExists() {
		fmt.Println("상태: 미설정")
		fmt.Println("API 키가 설정되지 않았습니다.")
		fmt.Println("'health-agent config --api-key <key>' 명령으로 설정하세요.")
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("상태: 오류\n%v\n", err)
		return
	}

	fmt.Println("상태: 설정됨")
	if len(cfg.APIKey) > 12 {
		fmt.Printf("API 키: %s****\n", cfg.APIKey[:12])
	}
	fmt.Printf("Agent ID: %s\n", config.LoadOrCreateAgentID())
	fmt.Printf("서버: %s\n", config.MonitoringAPIURL)
}

func cmdDocker() {
	// 1. API 키 확인
	apiKey, err := config.GetAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[INFO] API 키 확인됨 (%s****)\n", apiKey[:12])

	// 2. 옵션 파싱
	once := false
	for _, arg := range os.Args[2:] {
		if arg == "--once" {
			once = true
		}
	}

	// 3. 에이전트 실행
	agent := NewAgent(apiKey)
	agent.Run(once)
}

func cmdLxd() {
	fmt.Println("[INFO] LXD 모니터링은 아직 구현되지 않았습니다.")
	os.Exit(1)
}

type Agent struct {
	apiKey      string
	wsClient    *wsclient.Client
	osChecker   *oscheck.Checker
	dockerCheck *docker.Checker
	hostname    string
	agentID     string
	states      map[string]*types.ServiceState
}

func NewAgent(apiKey string) *Agent {
	hostname, _ := os.Hostname()
	agentID := config.LoadOrCreateAgentID()

	return &Agent{
		apiKey:      apiKey,
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

	// WebSocket 연결 (API 키 사용)
	var err error
	a.wsClient, err = wsclient.New(config.WebSocketURL, a.apiKey)
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
