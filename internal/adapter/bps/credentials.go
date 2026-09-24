package bps

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"bps-2api/internal/auth"
)

// ============================================================
// 凭据解析：账号池里可能存这些形态——
//   1. ChatGPT auth JSON：{access_token, refresh_token, chatgpt_account_id, email, ...}（用户直接粘贴导入）
//   2. 裸 access token（JWT，eyJ...）
//   3. 裸 refresh token（rt.1...）
//   4. 本适配器编码的 bundle JSON（刷新后回写，含 account_id/email/proxy）
// chatgpt_account_id 优先从 JWT claim https://api.openai.com/auth.chatgpt_account_id 推导。
// ============================================================

// Secret 是一个账号的认证材料。
type Secret struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"chatgpt_account_id,omitempty"`
	Email        string `json:"email,omitempty"`
	PlanType     string `json:"chatgpt_plan_type,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
}

// jwtClaims 是 ChatGPT access/id token 里用得到的字段。
type jwtClaims struct {
	Exp     int64  `json:"exp"`
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Auth    *authClaims    `json:"https://api.openai.com/auth"`
	Profile *profileClaims `json:"https://api.openai.com/profile"`
}

type authClaims struct {
	AccountID string `json:"chatgpt_account_id"`
	UserID    string `json:"chatgpt_user_id"`
	PlanType  string `json:"chatgpt_plan_type"`
}

type profileClaims struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ParseSecret 解析任意凭据字符串；解析不出内容时返回零值。
func ParseSecret(raw string) Secret {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Secret{}
	}
	if strings.HasPrefix(raw, "{") {
		if s, ok := parseSecretJSON(raw); ok {
			fillFromAccessToken(&s)
			return s
		}
	}
	if strings.Contains(raw, "access_token=") {
		s := Secret{
			AccessToken:  pickKV(raw, "access_token"),
			RefreshToken: pickKV(raw, "refresh_token"),
			AccountID:    pickKV(raw, "chatgpt_account_id"),
		}
		fillFromAccessToken(&s)
		return s
	}
	if strings.HasPrefix(raw, "rt.") {
		return Secret{RefreshToken: raw}
	}
	// 裸 access token（JWT 或 opaque）。
	s := Secret{AccessToken: raw}
	fillFromAccessToken(&s)
	return s
}

// parseSecretJSON 从 JSON 对象里取凭据字段（兼容 snake_case / camelCase）。
func parseSecretJSON(raw string) (Secret, bool) {
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return Secret{}, false
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	s := Secret{
		AccessToken:  str("access_token", "accessToken", "token"),
		RefreshToken: str("refresh_token", "refreshToken"),
		AccountID:    str("chatgpt_account_id", "chatgptAccountId", "account_id"),
		Email:        str("email"),
		PlanType:     str("plan_type", "planType"),
		Proxy:        str("proxy", "proxy_url", "proxyUrl"),
	}
	if s.AccessToken == "" && s.RefreshToken == "" {
		return Secret{}, false
	}
	return s, true
}

// pickKV 从 "k1=v1; k2=v2" 形态里取键值。
func pickKV(raw, name string) string {
	i := strings.Index(raw, name+"=")
	if i < 0 {
		return ""
	}
	v := raw[i+len(name)+1:]
	if j := strings.IndexAny(v, ";\"\n\r\t ,}"); j >= 0 {
		v = v[:j]
	}
	return strings.TrimSpace(v)
}

// fillFromAccessToken 用 access token 的 JWT 载荷补齐 account_id / email / plan。
func fillFromAccessToken(s *Secret) {
	if s == nil || s.AccessToken == "" {
		return
	}
	c := parseJWTClaims(s.AccessToken)
	if s.AccountID == "" && c.Auth != nil {
		s.AccountID = strings.TrimSpace(c.Auth.AccountID)
	}
	if s.Email == "" {
		if c.Profile != nil && strings.TrimSpace(c.Profile.Email) != "" {
			s.Email = strings.TrimSpace(c.Profile.Email)
		} else {
			s.Email = strings.TrimSpace(c.Email)
		}
	}
	if s.PlanType == "" && c.Auth != nil {
		s.PlanType = strings.TrimSpace(c.Auth.PlanType)
	}
}

// parseJWTClaims 解析 JWT 载荷；失败返回零值。
func parseJWTClaims(token string) jwtClaims {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return jwtClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return jwtClaims{}
		}
	}
	var c jwtClaims
	if json.Unmarshal(payload, &c) != nil {
		return jwtClaims{}
	}
	return c
}

// AccountID 从 access token 推导 chatgpt_account_id；失败返回空串。
func AccountID(accessToken string) string {
	c := parseJWTClaims(accessToken)
	if c.Auth == nil {
		return ""
	}
	return strings.TrimSpace(c.Auth.AccountID)
}

// Email 从 access token 推导邮箱；失败返回空串。
func Email(accessToken string) string {
	c := parseJWTClaims(accessToken)
	if c.Profile != nil && strings.TrimSpace(c.Profile.Email) != "" {
		return strings.TrimSpace(c.Profile.Email)
	}
	return strings.TrimSpace(c.Email)
}

// EncodeSecret 把凭据编码成 bundle JSON，用于写入 pool 的 RefreshToken 字段。
func EncodeSecret(s Secret) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return s.RefreshToken
	}
	return string(raw)
}

// BuildImportBundle 供导入链路调用：把 access/refresh/account/email/proxy 编成 bundle。
// 只有 refresh token 时也返回合法 bundle（账号在首次保活刷新后可用）。
func BuildImportBundle(accessToken, refreshToken, accountID, email, proxy string) string {
	s := Secret{
		AccessToken:  strings.TrimSpace(accessToken),
		RefreshToken: strings.TrimSpace(refreshToken),
		AccountID:    strings.TrimSpace(accountID),
		Email:        strings.TrimSpace(email),
		Proxy:        strings.TrimSpace(proxy),
	}
	fillFromAccessToken(&s)
	return EncodeSecret(s)
}

// TokenExpiry 返回 access token 的 exp（unix 秒）；无法解析返回 0。
func TokenExpiry(accessToken string) int64 {
	return auth.JWTExpiry(accessToken)
}

// newID 生成 16 字节随机 hex ID（task_id / turn_id / call_id 用）。
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte("bps-fallback-id"))
	}
	return hex.EncodeToString(b)
}
