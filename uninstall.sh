#!/bin/bash
set -e

# Docker Health Agent 삭제 스크립트
# 사용법: curl -sSL https://raw.githubusercontent.com/LodongDev/health-agent/main/uninstall.sh | bash

BINARY_NAME="docker-health-agent"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="$HOME/.docker-health-agent"

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }

echo ""
echo "=================================="
echo " Docker Health Agent 삭제"
echo "=================================="
echo ""

# 서비스 중지 (있으면)
if systemctl is-active --quiet docker-health-agent 2>/dev/null; then
    info "서비스 중지 중..."
    sudo systemctl stop docker-health-agent
    sudo systemctl disable docker-health-agent
    sudo rm -f /etc/systemd/system/docker-health-agent.service
    sudo systemctl daemon-reload
fi

# 바이너리 삭제
if [ -f "${INSTALL_DIR}/${BINARY_NAME}" ]; then
    info "바이너리 삭제 중..."
    sudo rm -f "${INSTALL_DIR}/${BINARY_NAME}"
fi

# 설정 파일 삭제
if [ -d "$CONFIG_DIR" ]; then
    info "설정 파일 삭제 중..."
    rm -rf "$CONFIG_DIR"
fi

info "삭제 완료!"
