package browser

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"health-agent/internal/types"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Checker 브라우저 기반 네트워크 체커
type Checker struct {
	timeout      time.Duration
	chromePath   string
	chromeFound  bool
	checkOnce    sync.Once
}

// New 브라우저 체커 생성
func New() *Checker {
	c := &Checker{
		timeout: 30 * time.Second,
	}
	c.detectChrome()
	return c
}

// IsAvailable Chrome이 설치되어 있는지 확인
func (c *Checker) IsAvailable() bool {
	return c.chromeFound
}

// GetChromePath Chrome 경로 반환
func (c *Checker) GetChromePath() string {
	return c.chromePath
}

// detectChrome Chrome/Chromium 설치 경로 탐지
func (c *Checker) detectChrome() {
	var paths []string

	switch runtime.GOOS {
	case "linux":
		paths = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
			"/usr/lib/chromium/chromium",
		}
	case "darwin":
		paths = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		paths = []string{
			os.Getenv("PROGRAMFILES") + "\\Google\\Chrome\\Application\\chrome.exe",
			os.Getenv("PROGRAMFILES(X86)") + "\\Google\\Chrome\\Application\\chrome.exe",
			os.Getenv("LOCALAPPDATA") + "\\Google\\Chrome\\Application\\chrome.exe",
		}
	}

	// 경로에서 Chrome 찾기
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			c.chromePath = p
			c.chromeFound = true
			log.Printf("[INFO] Chrome found: %s", p)
			return
		}
	}

	// which/where 명령으로 찾기
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("where", "chrome")
	} else {
		cmd = exec.Command("which", "google-chrome", "chromium", "chromium-browser")
	}

	if output, err := cmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			c.chromePath = strings.TrimSpace(lines[0])
			c.chromeFound = true
			log.Printf("[INFO] Chrome found via which/where: %s", c.chromePath)
			return
		}
	}

	log.Printf("[WARN] Chrome not found. Web resource checking will use fallback HTML parsing.")
}

// GetInstallCommand Chrome 설치 명령어 반환
func (c *Checker) GetInstallCommand() string {
	switch runtime.GOOS {
	case "linux":
		return `# Debian/Ubuntu:
sudo apt-get update && sudo apt-get install -y chromium-browser

# CentOS/RHEL:
sudo yum install -y chromium

# Alpine:
apk add chromium`
	case "darwin":
		return "brew install --cask google-chrome"
	case "windows":
		return "winget install Google.Chrome"
	default:
		return "Please install Chrome or Chromium manually"
	}
}

// CheckPageResources 웹 페이지의 모든 네트워크 요청을 캡처하고 4xx/5xx 에러 반환
func (c *Checker) CheckPageResources(pageURL string) ([]types.ResourceError, error) {
	if !c.chromeFound {
		return nil, fmt.Errorf("Chrome not installed")
	}

	var errors []types.ResourceError
	var mu sync.Mutex

	// Chrome 옵션 설정
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.chromePath),
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.NoSandbox, // Docker/Linux 환경에서 필요
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 타임아웃 설정
	ctx, cancel = context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// 네트워크 이벤트 리스너 등록
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventResponseReceived:
			statusCode := int(e.Response.Status)
			if statusCode >= 400 {
				mu.Lock()
				errors = append(errors, types.ResourceError{
					URL:        e.Response.URL,
					StatusCode: statusCode,
					Type:       getResourceType(e.Type),
				})
				mu.Unlock()
				log.Printf("[WARN] Network error: %d %s (%s)", statusCode, e.Type, truncateURL(e.Response.URL))
			}
		case *network.EventLoadingFailed:
			// 로딩 실패 (연결 거부, 타임아웃 등)
			mu.Lock()
			errors = append(errors, types.ResourceError{
				URL:        e.ErrorText,
				StatusCode: 0,
				Type:       string(e.Type),
			})
			mu.Unlock()
			log.Printf("[WARN] Network failed: %s (%s)", e.ErrorText, e.Type)
		}
	})

	// 네트워크 활성화 및 페이지 로드
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(pageURL),
		// 페이지 로드 완료 대기 (DOMContentLoaded + 추가 리소스 로딩)
		chromedp.Sleep(3*time.Second),
	)

	if err != nil {
		// 타임아웃이나 에러가 발생해도 수집된 에러는 반환
		if len(errors) > 0 {
			return errors, nil
		}
		return nil, fmt.Errorf("page load failed: %v", err)
	}

	return errors, nil
}

// getResourceType 네트워크 리소스 타입을 문자열로 변환
func getResourceType(t network.ResourceType) string {
	switch t {
	case network.ResourceTypeDocument:
		return "document"
	case network.ResourceTypeStylesheet:
		return "css"
	case network.ResourceTypeImage:
		return "img"
	case network.ResourceTypeMedia:
		return "media"
	case network.ResourceTypeFont:
		return "font"
	case network.ResourceTypeScript:
		return "js"
	case network.ResourceTypeXHR:
		return "xhr"
	case network.ResourceTypeFetch:
		return "fetch"
	case network.ResourceTypeWebSocket:
		return "websocket"
	case network.ResourceTypeManifest:
		return "manifest"
	default:
		return string(t)
	}
}

// truncateURL URL이 너무 길면 축약
func truncateURL(url string) string {
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
}
