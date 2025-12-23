# Health Agent 배포 가이드

## 새 서버에 설치

### 방법 1: YUM 저장소 등록 (권장)

```bash
# 저장소 등록
sudo curl -o /etc/yum.repos.d/health-agent.repo https://lodongdev.github.io/health-agent/health-agent.repo

# 설치
sudo yum install health-agent -y

# API 키 설정
health-agent config --api-key YOUR_API_KEY

# 서비스 시작
health-agent docker
```

### 방법 2: 직접 다운로드

```bash
# 다운로드
curl -sSL https://github.com/LodongDev/health-agent/releases/latest/download/health-agent-linux-amd64 -o /usr/local/bin/health-agent

# 실행 권한
chmod +x /usr/local/bin/health-agent

# API 키 설정
health-agent config --api-key YOUR_API_KEY

# 서비스 시작 (systemd 등록 포함)
health-agent docker
```

### 설치 확인

```bash
health-agent version
systemctl status health-agent
```

---

## 새 버전 배포 방법 (개발자용)

### 1. 코드 수정 후 버전 업데이트

`cmd/agent/main.go` 파일에서 버전 수정:
```go
const version = "1.18.0"  // 버전 번호 증가
```

### 2. 커밋 및 태그 생성

```bash
cd C:\Users\cyj\Downloads\docker-health-agent

# 변경사항 커밋
git add .
git commit -m "feat: 새 기능 설명 (v1.18.0)"

# 태그 생성 및 푸시
git tag v1.18.0
git push origin main
git push origin v1.18.0
```

### 3. 자동 빌드 확인

태그 푸시 후 GitHub Actions가 자동 실행됩니다:
- 빌드 상태: https://github.com/LodongDev/health-agent/actions
- 완료까지 약 2-3분 소요

### 4. 빌드 결과물

자동 생성되는 파일:
- `health-agent-1.18.0-1.x86_64.rpm` (CentOS/RHEL)
- `health-agent-1.18.0-1.aarch64.rpm` (ARM64)
- `health-agent_1.18.0_linux_amd64.tar.gz`
- `health-agent_1.18.0_linux_arm64.tar.gz`

다운로드: https://github.com/LodongDev/health-agent/releases

---

## 서버에서 업데이트

### 방법 1: YUM (권장)

```bash
yum clean all && yum update health-agent -y
systemctl restart health-agent
health-agent version
```

### 방법 2: 직접 다운로드

```bash
# 다운로드
curl -sSL https://github.com/LodongDev/health-agent/releases/download/v1.18.0/health-agent-linux-amd64 -o /usr/local/bin/health-agent

# 실행 권한
chmod +x /usr/local/bin/health-agent

# 서비스 재시작
systemctl restart health-agent

# 버전 확인
health-agent version
```

---

## 요약 (한 줄 명령어)

```bash
# 배포 (Windows에서)
git add . && git commit -m "v1.18.0" && git tag v1.18.0 && git push origin main --tags

# 서버 업데이트 (Linux에서)
yum clean all && yum update health-agent -y && systemctl restart health-agent
```
