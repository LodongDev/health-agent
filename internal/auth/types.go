package auth

import "time"

// TokenData 저장되는 토큰 정보
type TokenData struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Email        string    `json:"email"`
}

// LoginRequest 로그인 요청
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// TokenResponse 토큰 응답 (lodong_auth)
type TokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"` // 초 단위
}

// AuthResponse 인증 응답
type AuthResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
}

// APIResponse lodong_auth API 응답 래퍼
type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// MemberResponse 사용자 정보 응답
type MemberResponse struct {
	UUID       string `json:"uuid"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Department string `json:"department"`
	MemberType string `json:"memberType"`
	Role       string `json:"role"`
	Status     string `json:"status"`
}

// IsExpired 토큰 만료 여부 확인
func (t *TokenData) IsExpired() bool {
	// 만료 5분 전부터 갱신 필요로 판단
	return time.Now().Add(5 * time.Minute).After(t.ExpiresAt)
}

// IsValid 토큰 유효성 확인
func (t *TokenData) IsValid() bool {
	return t.AccessToken != "" && !t.IsExpired()
}
