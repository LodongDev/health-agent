.PHONY: build build-linux clean install

BINARY=docker-health-agent
VERSION=1.0.0

build:
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/agent

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY) ./cmd/agent

build-arm:
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY)-arm64 ./cmd/agent

clean:
	rm -f $(BINARY) $(BINARY)-arm64

install: build-linux
	sudo cp $(BINARY) /usr/local/bin/
	sudo chmod +x /usr/local/bin/$(BINARY)
	sudo mkdir -p /etc/docker-health-agent
	@if [ ! -f /etc/systemd/system/docker-health-agent.service ]; then \
		sudo cp docker-health-agent.service /etc/systemd/system/; \
		sudo systemctl daemon-reload; \
	fi
	@echo "✓ 설치 완료"
	@echo "  1. /etc/docker-health-agent/env 파일 생성 후 설정"
	@echo "  2. sudo systemctl enable --now docker-health-agent"

uninstall:
	sudo systemctl stop docker-health-agent || true
	sudo systemctl disable docker-health-agent || true
	sudo rm -f /usr/local/bin/$(BINARY)
	sudo rm -f /etc/systemd/system/docker-health-agent.service
	sudo systemctl daemon-reload
	@echo "✓ 제거 완료"

test:
	./$(BINARY) --api-url http://localhost:8080/api --once

deps:
	go mod download
	go mod tidy
