# Docker Health Agent (Go CLI)

## 프로젝트 개요
리눅스 서버에서 Docker 컨테이너를 모니터링하고 중앙 서버로 전송하는 CLI 에이전트

**로그인 필수**: lodong_auth 서버를 통한 인증 후 사용 가능

### 아키텍처
```
[사용자]                          [인증 서버]
┌──────────────────┐             ┌─────────────────────────┐
│ docker-health-agent login      │             │ lodong_auth             │
│   - 이메일/비밀번호 입력       │ ──POST──▶   │ 172.27.50.118:10709     │
│   - 토큰 저장                  │             └─────────────────────────┘
└──────────────────┘

[리눅스 서버]                     [중앙 서버]
┌──────────────────┐             ┌─────────────────────────┐
│ docker-health-agent run        │             │ gomtang-alert API       │
│   - Docker 감시                │ ──POST──▶   │ 172.27.1.1:11401        │
│   - 타입 판별                  │             └─────────────────────────┘
│   - 헬스체크                   │
└──────────────────┘
```

### 핵심 흐름
1. **로그인** (최초 1회)
2. Docker API로 컨테이너 발견
3. Label > Image > Port 순으로 타입 판별
4. 타입별 헬스체크 (API→HTTP, DB→TCP, Redis→PING)
5. 결과를 중앙 서버로 POST
6. 상태 변경 시 즉시 알림

## 기술 스택
- Go 1.21+
- docker/docker (Docker API)
- golang.org/x/term (비밀번호 입력)
- 표준 라이브러리 (net/http, encoding/json)

## 디렉토리 구조
```
├── cmd/agent/main.go      # 진입점, 서브커맨드 처리
├── internal/
│   ├── auth/              # 인증 모듈 (NEW)
│   │   ├── auth.go        # 로그인/토큰 갱신
│   │   ├── token.go       # 토큰 저장/로드
│   │   └── types.go       # 인증 타입
│   ├── types/types.go     # 공통 타입
│   ├── config/config.go   # 설정
│   ├── discovery/         # Docker 컨테이너 발견
│   ├── resolver/          # 타입 판별
│   ├── checker/           # 헬스체크
│   └── client/            # API 클라이언트
├── .goreleaser.yaml       # GoReleaser 설정
├── .github/workflows/     # GitHub Actions
├── go.mod
├── Makefile
└── install.sh
```

## 빌드 & 실행
```bash
# 빌드
make build
# 또는
go build -o docker-health-agent ./cmd/agent

# 1. 로그인 (최초 1회)
./docker-health-agent login
# Email: user@example.com
# Password: ********

# 2. 에이전트 실행
./docker-health-agent run \
  --api-url http://172.27.1.1:11401/api/gomtang-alert \
  --interval 30s

# 로그인 상태 확인
./docker-health-agent whoami

# 로그아웃
./docker-health-agent logout

# 도움말
./docker-health-agent help
./docker-health-agent run --help
```

## CLI 명령어
```
docker-health-agent <command> [options]

Commands:
  login     로그인 (이메일/비밀번호)
  logout    로그아웃
  whoami    현재 로그인 상태 확인
  run       에이전트 실행 (로그인 필수)
  version   버전 정보
  help      도움말
```

## run 옵션
```
--auth-url      인증 서버 URL (기본: http://172.27.50.118:10709)
--api-url       중앙 서버 URL (필수)
--api-token     API 인증 토큰
--interval      체크 주기 (기본: 30s)
--docker-sock   Docker 소켓 (기본: /var/run/docker.sock)
--label-prefix  Label prefix (기본: monitor)
--log-level     로그 레벨 (debug/info/warn/error)
--once          한 번만 실행 후 종료
```

## 환경변수
```
AUTH_URL        인증 서버 URL
API_URL         중앙 서버 URL
API_TOKEN       API 토큰
DOCKER_SOCK     Docker 소켓 경로
LOG_LEVEL       로그 레벨
```

## 토큰 저장 위치
- Linux/Mac: `~/.docker-health-agent/token.json`
- Windows: `%USERPROFILE%\.docker-health-agent\token.json`

## Label 규칙
```yaml
labels:
  monitor.type: api          # 타입 명시
  monitor.health: /health    # 커스텀 헬스 엔드포인트
  monitor.exclude: "true"    # 모니터링 제외
```

## API 통신
```
POST /agents/register        # 에이전트 등록
POST /containers/report      # 상태 보고
POST /alerts                 # 상태 변경 알림
```

## GitHub Releases 배포
```bash
# 태그 생성 후 자동 빌드/배포
git tag v1.0.0
git push origin v1.0.0

# 수동 릴리스 (로컬)
goreleaser release --clean
```

지원 플랫폼:
- linux/amd64, linux/arm64
- darwin/amd64, darwin/arm64
- windows/amd64

## 수동 배포
```bash
# 빌드
GOOS=linux GOARCH=amd64 go build -o docker-health-agent ./cmd/agent

# 서버에 복사
scp docker-health-agent user@server:/usr/local/bin/

# systemd 서비스 설치
sudo cp docker-health-agent.service /etc/systemd/system/
sudo systemctl enable --now docker-health-agent
```

## TODO
1. [x] 타입 정의
2. [x] 설정/CLI 파싱
3. [x] Docker 발견
4. [x] 타입 판별
5. [x] 헬스체크
6. [x] API 클라이언트
7. [x] 메인 루프
8. [x] 인증 모듈 (lodong_auth 연동)
9. [x] GitHub Releases 배포 설정
10. [ ] 테스트
