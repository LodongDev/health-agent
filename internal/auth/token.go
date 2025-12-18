package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const tokenFileName = "token.json"

// getTokenDir 토큰 저장 디렉토리 경로
func getTokenDir() string {
	var homeDir string

	if runtime.GOOS == "windows" {
		homeDir = os.Getenv("USERPROFILE")
	} else {
		homeDir = os.Getenv("HOME")
	}

	return filepath.Join(homeDir, ".docker-health-agent")
}

// getTokenPath 토큰 파일 전체 경로
func getTokenPath() string {
	return filepath.Join(getTokenDir(), tokenFileName)
}

// SaveToken 토큰을 파일에 저장
func SaveToken(token *TokenData) error {
	dir := getTokenDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("디렉토리 생성 실패: %w", err)
	}

	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	tokenPath := getTokenPath()
	if err := os.WriteFile(tokenPath, data, 0600); err != nil {
		return fmt.Errorf("파일 저장 실패: %w", err)
	}

	return nil
}

// LoadToken 저장된 토큰 로드
func LoadToken() (*TokenData, error) {
	tokenPath := getTokenPath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("로그인이 필요합니다. 'docker-health-agent login' 실행")
		}
		return nil, fmt.Errorf("토큰 파일 읽기 실패: %w", err)
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("토큰 파싱 실패: %w", err)
	}

	return &token, nil
}

// DeleteToken 토큰 파일 삭제
func DeleteToken() error {
	tokenPath := getTokenPath()

	if err := os.Remove(tokenPath); err != nil {
		if os.IsNotExist(err) {
			return nil // 이미 삭제됨
		}
		return fmt.Errorf("토큰 삭제 실패: %w", err)
	}

	return nil
}

// TokenExists 토큰 파일 존재 여부
func TokenExists() bool {
	tokenPath := getTokenPath()
	_, err := os.Stat(tokenPath)
	return err == nil
}
