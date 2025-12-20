package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"health-agent/internal/config"
	"health-agent/internal/docker"
	"health-agent/internal/oscheck"
	"health-agent/internal/types"
	"health-agent/internal/wsclient"
)

const version = "1.9.0"

const serviceFile = `[Unit]
Description=Health Agent - Service Health Check Agent
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
ExecStart=/usr/bin/health-agent docker --foreground
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

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
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Health Agent - Service Health Check Agent")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  health-agent <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  config    Configure API key")
	fmt.Println("            --api-key <key>  Set API key")
	fmt.Println("            --show           Show current config")
	fmt.Println()
	fmt.Println("  status    Current configuration status")
	fmt.Println()
	fmt.Println("  docker    Docker container + OS service monitoring")
	fmt.Println("            (default: install as systemd service)")
	fmt.Println("            --foreground     Run in foreground (no service install)")
	fmt.Println("            --once           Run once and exit")
	fmt.Println("            --stop           Stop the service")
	fmt.Println("            --uninstall      Remove the service")
	fmt.Println()
	fmt.Println("  lxd       LXD container + OS service monitoring (planned)")
	fmt.Println()
	fmt.Println("  version   Version info")
	fmt.Println("  help      Help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  health-agent config --api-key ldk_xxxxx")
	fmt.Println("  health-agent docker              # Install and start as service")
	fmt.Println("  health-agent docker --foreground # Run in foreground")
	fmt.Println("  health-agent docker --stop       # Stop service")
	fmt.Println("  health-agent docker --uninstall  # Remove service")
}

func cmdConfig() {
	if len(os.Args) < 3 {
		cmdStatus()
		return
	}

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--api-key":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "Please enter API key")
				os.Exit(1)
			}
			apiKey := os.Args[i+1]
			if apiKey == "" || !strings.HasPrefix(apiKey, "ldk_") {
				fmt.Fprintln(os.Stderr, "Invalid API key format (must start with ldk_)")
				os.Exit(1)
			}

			cfg := &config.AgentConfig{APIKey: apiKey}
			if err := config.SaveConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to save config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[INFO] API key configured\n")
			fmt.Printf("       Key: %s****\n", apiKey[:12])
			return

		case "--show":
			cmdStatus()
			return
		}
	}
}

func cmdStatus() {
	if !config.ConfigExists() {
		fmt.Println("Status: Not configured")
		fmt.Println("API key not set.")
		fmt.Println("Use 'health-agent config --api-key <key>' to configure.")
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Status: Error\n%v\n", err)
		return
	}

	fmt.Println("Status: Configured")
	if len(cfg.APIKey) > 12 {
		fmt.Printf("API Key: %s****\n", cfg.APIKey[:12])
	}
	fmt.Printf("Agent ID: %s\n", config.LoadOrCreateAgentID())
	fmt.Printf("Server: %s\n", config.MonitoringAPIURL)

	if runtime.GOOS == "linux" {
		if isServiceInstalled() {
			if isServiceRunning() {
				fmt.Println("Service: Running")
			} else {
				fmt.Println("Service: Stopped")
			}
		} else {
			fmt.Println("Service: Not installed")
		}
	}
}

func cmdDocker() {
	apiKey, err := config.GetAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[INFO] API key verified (%s****)\n", apiKey[:12])

	once := false
	foreground := false
	stopService := false
	uninstall := false

	for _, arg := range os.Args[2:] {
		switch arg {
		case "--once":
			once = true
		case "--foreground":
			foreground = true
		case "--stop":
			stopService = true
		case "--uninstall":
			uninstall = true
		}
	}

	if stopService {
		cmdStopService()
		return
	}

	if uninstall {
		cmdUninstallService()
		return
	}

	if runtime.GOOS == "linux" && !foreground && !once {
		if os.Geteuid() != 0 {
			fmt.Println("[INFO] Not running as root. Starting in foreground mode.")
			fmt.Println("[INFO] Run with sudo to install as systemd service.")
		} else {
			if err := installAndStartService(); err != nil {
				fmt.Fprintf(os.Stderr, "[ERROR] Service install failed: %v\n", err)
				fmt.Println("[INFO] Falling back to foreground mode...")
			} else {
				fmt.Println("[INFO] Service installed and started successfully!")
				fmt.Println("[INFO] Use 'health-agent docker --stop' to stop")
				fmt.Println("[INFO] Use 'health-agent docker --uninstall' to remove")
				fmt.Println("[INFO] Use 'journalctl -u health-agent -f' to view logs")
				return
			}
		}
	}

	agent := NewAgent(apiKey)
	agent.Run(once)
}

func cmdLxd() {
	fmt.Println("[INFO] LXD monitoring is not implemented yet.")
	os.Exit(1)
}

func isServiceInstalled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat("/etc/systemd/system/health-agent.service")
	return err == nil
}

func isServiceRunning() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	cmd := exec.Command("systemctl", "is-active", "--quiet", "health-agent")
	return cmd.Run() == nil
}

func installAndStartService() error {
	fmt.Println("[INFO] Installing systemd service...")

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	if execPath != "/usr/bin/health-agent" {
		fmt.Println("[INFO] Copying binary to /usr/bin/health-agent...")
		input, err := os.ReadFile(execPath)
		if err != nil {
			return fmt.Errorf("failed to read binary: %w", err)
		}
		if err := os.WriteFile("/usr/bin/health-agent", input, 0755); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	fmt.Println("[INFO] Creating service file...")
	if err := os.WriteFile("/etc/systemd/system/health-agent.service", []byte(serviceFile), 0644); err != nil {
		return fmt.Errorf("failed to create service file: %w", err)
	}

	fmt.Println("[INFO] Reloading systemd...")
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	fmt.Println("[INFO] Enabling service...")
	if err := exec.Command("systemctl", "enable", "health-agent").Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	fmt.Println("[INFO] Starting service...")
	if err := exec.Command("systemctl", "start", "health-agent").Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

func cmdStopService() {
	if runtime.GOOS != "linux" {
		fmt.Println("[ERROR] Service management is only available on Linux")
		os.Exit(1)
	}

	if !isServiceInstalled() {
		fmt.Println("[INFO] Service is not installed")
		return
	}

	fmt.Println("[INFO] Stopping service...")
	if err := exec.Command("systemctl", "stop", "health-agent").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Failed to stop service: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[INFO] Service stopped")
}

func cmdUninstallService() {
	if runtime.GOOS != "linux" {
		fmt.Println("[ERROR] Service management is only available on Linux")
		os.Exit(1)
	}

	if !isServiceInstalled() {
		fmt.Println("[INFO] Service is not installed")
		return
	}

	if os.Geteuid() != 0 {
		fmt.Println("[ERROR] Root privileges required. Run with sudo.")
		os.Exit(1)
	}

	fmt.Println("[INFO] Stopping service...")
	exec.Command("systemctl", "stop", "health-agent").Run()

	fmt.Println("[INFO] Disabling service...")
	exec.Command("systemctl", "disable", "health-agent").Run()

	fmt.Println("[INFO] Removing service file...")
	os.Remove("/etc/systemd/system/health-agent.service")

	fmt.Println("[INFO] Reloading systemd...")
	exec.Command("systemctl", "daemon-reload").Run()

	fmt.Println("[INFO] Service uninstalled successfully")
	fmt.Println("[INFO] Binary at /usr/bin/health-agent was not removed")
}

type Agent struct {
	apiKey      string
	wsClient    *wsclient.Client
	osChecker   *oscheck.Checker
	dockerCheck *docker.Checker
	hostname    string
	ip          string
	agentID     string
	states      map[string]*types.ServiceState
}

func NewAgent(apiKey string) *Agent {
	hostname, _ := os.Hostname()
	agentID := config.LoadOrCreateAgentID()
	ip := config.GetLocalIP()

	return &Agent{
		apiKey:      apiKey,
		osChecker:   oscheck.New(),
		dockerCheck: docker.New(),
		hostname:    hostname,
		ip:          ip,
		agentID:     agentID,
		states:      make(map[string]*types.ServiceState),
	}
}

func (a *Agent) Run(once bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	a.printBanner()

	var err error
	a.wsClient, err = wsclient.New(config.WebSocketURL, a.apiKey)
	if err != nil {
		log.Fatalf("[ERROR] WebSocket connection failed: %v", err)
	}
	defer a.wsClient.Close()
	log.Println("[INFO] Server connected")

	if err := a.dockerCheck.Ping(ctx); err != nil {
		log.Printf("[WARN] Docker connection failed: %v (skipping Docker checks)", err)
	} else {
		log.Println("[INFO] Docker connected")
	}

	if once {
		a.runOnce(ctx)
		return
	}

	checkTicker := time.NewTicker(30 * time.Second)
	defer checkTicker.Stop()

	log.Println("[INFO] Monitoring started (30s interval)")

	a.check(ctx)

	for {
		select {
		case <-checkTicker.C:
			a.check(ctx)
		case <-sigCh:
			log.Println("\n[INFO] Shutting down...")
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

	log.Println("[INFO] Checking OS services...")
	osResults := a.osChecker.CheckAll()
	for _, r := range osResults {
		results = append(results, r)
		a.handleStateChange(r)
	}

	log.Println("[INFO] Checking Docker containers...")
	dockerResults, err := a.dockerCheck.CheckAll(ctx)
	if err != nil {
		log.Printf("[WARN] Docker check failed: %v", err)
	} else {
		for _, r := range dockerResults {
			results = append(results, r)
			a.handleStateChange(r)
		}
	}

	if err := a.sendResults(results); err != nil {
		log.Printf("[ERROR] Failed to send results: %v", err)
	}

	log.Printf("[INFO] Check complete: %d services, %v", len(results), time.Since(start).Round(time.Millisecond))
}

func (a *Agent) handleStateChange(current types.ServiceState) {
	prev, exists := a.states[current.ID]

	a.states[current.ID] = &current

	if !exists {
		return
	}

	if prev.Status != current.Status {
		log.Printf("[ALERT] %s: %s -> %s", current.Name, prev.Status, current.Status)
	}
}

func (a *Agent) sendResults(results []types.ServiceState) error {
	payload := types.AgentReport{
		AgentID:   a.agentID,
		Hostname:  a.hostname,
		IP:        a.ip,
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
	fmt.Printf(" IP       : %s\n", a.ip)
	fmt.Printf(" Server   : %s\n", config.MonitoringAPIURL)
	fmt.Println("==========================================")
}

func (a *Agent) printSummary() {
	fmt.Println("\nSummary:")
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
	fmt.Printf("Total %d | UP: %d | DOWN: %d | WARN: %d\n", len(a.states), up, down, warn)
}
