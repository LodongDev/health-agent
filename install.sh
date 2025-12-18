#!/bin/bash
set -e

# Docker Health Agent 설치 스크립트
# 사용법: curl -sSL https://raw.githubusercontent.com/LodongDev/health-agent/main/install.sh | bash

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo ""
echo "=================================="
echo " Docker Health Agent 설치"
echo "=================================="
echo ""

# root 권한 확인
if [ "$EUID" -ne 0 ]; then
    SUDO="sudo"
else
    SUDO=""
fi

# OS 확인
if [ -f /etc/redhat-release ]; then
    OS="rhel"
elif [ -f /etc/debian_version ]; then
    OS="debian"
else
    error "지원하지 않는 OS입니다 (RHEL/CentOS/Debian/Ubuntu만 지원)"
fi

if [ "$OS" = "rhel" ]; then
    info "YUM 레포 등록 중..."
    $SUDO curl -sSL -o /etc/yum.repos.d/health-agent.repo https://lodongdev.github.io/health-agent/health-agent.repo

    info "패키지 설치 중..."
    $SUDO yum install -y docker-health-agent

elif [ "$OS" = "debian" ]; then
    info "바이너리 다운로드 중..."

    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) error "지원하지 않는 아키텍처: $ARCH" ;;
    esac

    VERSION=$(curl -sSL "https://api.github.com/repos/LodongDev/health-agent/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    VERSION_NUM="${VERSION#v}"

    TMP_DIR=$(mktemp -d)
    cd "$TMP_DIR"
    curl -sSLO "https://github.com/LodongDev/health-agent/releases/download/${VERSION}/docker-health-agent_${VERSION_NUM}_linux_${ARCH}.tar.gz"
    tar -xzf "docker-health-agent_${VERSION_NUM}_linux_${ARCH}.tar.gz"
    $SUDO mv docker-health-agent /usr/local/bin/
    $SUDO chmod +x /usr/local/bin/docker-health-agent
    rm -rf "$TMP_DIR"
fi

# 설치 확인
if command -v docker-health-agent &> /dev/null; then
    echo ""
    info "설치 완료!"
    docker-health-agent version
    echo ""
    echo "사용법:"
    echo "  1. 로그인:  docker-health-agent login"
    echo "  2. 실행:    docker-health-agent run --api-url http://your-server/api"
    echo ""
else
    error "설치 실패"
fi
