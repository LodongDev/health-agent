#!/bin/bash
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo ""
echo "=================================="
echo " Health Agent 설치"
echo "=================================="
echo ""

if [ "$EUID" -ne 0 ]; then
    SUDO="sudo"
else
    SUDO=""
fi

if [ -f /etc/redhat-release ]; then
    OS="rhel"
elif [ -f /etc/debian_version ]; then
    OS="debian"
else
    error "지원하지 않는 OS입니다 (RHEL/CentOS/Debian/Ubuntu만 지원)"
fi

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64"; RPM_ARCH="x86_64" ;;
    aarch64|arm64) ARCH="arm64"; RPM_ARCH="aarch64" ;;
    *) error "지원하지 않는 아키텍처: $ARCH" ;;
esac

VERSION=$(curl -sSL "https://api.github.com/repos/LodongDev/health-agent/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
VERSION_NUM="${VERSION#v}"

info "버전: $VERSION ($ARCH)"

if [ "$OS" = "rhel" ]; then
    info "RPM 패키지 다운로드 중..."
    TMP_DIR=$(mktemp -d)
    cd "$TMP_DIR"
    curl -sSLO "https://github.com/LodongDev/health-agent/releases/download/${VERSION}/health-agent-${VERSION_NUM}-1.${RPM_ARCH}.rpm"

    info "패키지 설치 중..."
    $SUDO rpm -Uvh --force "health-agent-${VERSION_NUM}-1.${RPM_ARCH}.rpm"
    rm -rf "$TMP_DIR"

elif [ "$OS" = "debian" ]; then
    info "바이너리 다운로드 중..."
    TMP_DIR=$(mktemp -d)
    cd "$TMP_DIR"
    curl -sSLO "https://github.com/LodongDev/health-agent/releases/download/${VERSION}/health-agent_${VERSION_NUM}_linux_${ARCH}.tar.gz"
    tar -xzf "health-agent_${VERSION_NUM}_linux_${ARCH}.tar.gz"
    $SUDO mv health-agent /usr/local/bin/
    $SUDO chmod +x /usr/local/bin/health-agent
    rm -rf "$TMP_DIR"
fi

if [ -x /usr/local/bin/health-agent ]; then
    hash -r 2>/dev/null || true
    echo ""
    info "설치 완료!"
    /usr/local/bin/health-agent version
    echo ""
    echo "사용법:"
    echo "  1. 로그인:  health-agent login"
    echo "  2. 실행:    health-agent docker"
    echo ""
else
    error "설치 실패"
fi
