package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client 인증 클라이언트
type Client struct {
	authURL    string
	httpClient *http.Client
}

// NewClient 새 인증 클라이언트 생성
func NewClient(authURL string) *Client {
	return &Client{
		authURL: authURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Login 로그인 요청
func (c *Client) Login(email, password string) (*TokenData, error) {
	reqBody := LoginRequest{
		Email:    email,
		Password: password,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("요청 생성 실패: %w", err)
	}

	resp, err := c.httpClient.Post(
		c.authURL+"/api/auth/login",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("서버 연결 실패: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("응답 읽기 실패: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp APIResponse[any]
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("%s", errResp.Message)
		}
		return nil, fmt.Errorf("로그인 실패 (HTTP %d)", resp.StatusCode)
	}

	var apiResp APIResponse[AuthResponse]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("응답 파싱 실패: %w", err)
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("%s", apiResp.Message)
	}

	// 만료 시간 계산
	expiresAt := time.Now().Add(time.Duration(apiResp.Data.ExpiresIn) * time.Second)

	token := &TokenData{
		AccessToken:  apiResp.Data.AccessToken,
		RefreshToken: apiResp.Data.RefreshToken,
		TokenType:    apiResp.Data.TokenType,
		ExpiresAt:    expiresAt,
		Email:        email,
	}

	return token, nil
}

// RefreshToken 토큰 갱신
func (c *Client) RefreshToken(refreshToken string) (*TokenData, string, error) {
	req, err := http.NewRequest("POST", c.authURL+"/api/auth/token/refresh", nil)
	if err != nil {
		return nil, "", fmt.Errorf("요청 생성 실패: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("서버 연결 실패: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("응답 읽기 실패: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("토큰 갱신 실패 (HTTP %d)", resp.StatusCode)
	}

	var apiResp APIResponse[TokenResponse]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, "", fmt.Errorf("응답 파싱 실패: %w", err)
	}

	if !apiResp.Success {
		return nil, "", fmt.Errorf("%s", apiResp.Message)
	}

	expiresAt := time.Now().Add(time.Duration(apiResp.Data.ExpiresIn) * time.Second)

	token := &TokenData{
		AccessToken:  apiResp.Data.AccessToken,
		RefreshToken: apiResp.Data.RefreshToken,
		TokenType:    apiResp.Data.TokenType,
		ExpiresAt:    expiresAt,
	}

	return token, "", nil
}

// GetMe 현재 사용자 정보 조회
func (c *Client) GetMe(accessToken string) (*MemberResponse, error) {
	req, err := http.NewRequest("GET", c.authURL+"/api/auth/me", nil)
	if err != nil {
		return nil, fmt.Errorf("요청 생성 실패: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("서버 연결 실패: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("응답 읽기 실패: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("사용자 정보 조회 실패 (HTTP %d)", resp.StatusCode)
	}

	var apiResp APIResponse[MemberResponse]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("응답 파싱 실패: %w", err)
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("%s", apiResp.Message)
	}

	return &apiResp.Data, nil
}

// EnsureValidToken 토큰 유효성 확인 및 자동 갱신
func EnsureValidToken(authURL string) (*TokenData, error) {
	token, err := LoadToken()
	if err != nil {
		return nil, err
	}

	// 토큰이 아직 유효하면 그대로 반환
	if token.IsValid() {
		return token, nil
	}

	// RefreshToken으로 갱신 시도
	if token.RefreshToken == "" {
		return nil, fmt.Errorf("토큰이 만료되었습니다. 다시 로그인하세요")
	}

	client := NewClient(authURL)
	newToken, _, err := client.RefreshToken(token.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("토큰 갱신 실패: %w. 다시 로그인하세요", err)
	}

	// 기존 이메일 유지
	newToken.Email = token.Email

	// 새 토큰 저장
	if err := SaveToken(newToken); err != nil {
		return nil, fmt.Errorf("토큰 저장 실패: %w", err)
	}

	return newToken, nil
}
