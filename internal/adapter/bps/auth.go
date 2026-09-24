package bps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bps-2api/internal/adapter"
	"bps-2api/internal/auth"
)

// ============================================================
// 站点认证：只支持凭据导入（access_token / refresh_token），无浏览器登录。
// token 过期时由内核保活/请求路径调用 ExchangeCredential 走 OAuth 刷新。
// ============================================================

// LoginURL 无浏览器登录流程，返回空串。
func (a *Adapter) LoginURL(websiteURL, challenge, uuid string) string { return "" }

// PollLogin 无浏览器登录流程。
func (a *Adapter) PollLogin(apiBaseURL, uuid, verifier, clientVersion, clientType string, client *http.Client) (*auth.Token, error) {
	return nil, adapter.ErrNotImplemented
}

// ExchangeCredential 校验/刷新凭据：
//   - secret 含 refresh token → OAuth 刷新（返回轮换后的 bundle）
//   - secret 只有 access token → 校验未过期后原样返回
//
// mock 模式下只做本地解析，不发网络请求。
func (a *Adapter) ExchangeCredential(apiBaseURL, secret, clientVersion, clientType string, client *http.Client) (*auth.Token, error) {
	sec := ParseSecret(secret)
	if sec.AccessToken == "" && sec.RefreshToken == "" {
		return nil, fmt.Errorf("bps: empty credential (need access_token or refresh_token)")
	}
	if strings.TrimSpace(sec.RefreshToken) != "" {
		if a.mock {
			return &auth.Token{AccessToken: firstNonEmpty(sec.AccessToken, "mock-access-token"), RefreshToken: EncodeSecret(sec)}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := RefreshAccessToken(ctx, sec.RefreshToken, client)
		if err != nil {
			// 有 refresh token 但刷新失败：若本地 access token 还没过期，先放行使用。
			if sec.AccessToken != "" && !tokenExpired(sec.AccessToken, 0) {
				return &auth.Token{AccessToken: sec.AccessToken, RefreshToken: EncodeSecret(sec)}, nil
			}
			return nil, err
		}
		sec.AccessToken = res.AccessToken
		sec.RefreshToken = res.RefreshToken
		fillFromAccessToken(&sec)
		return &auth.Token{AccessToken: sec.AccessToken, RefreshToken: EncodeSecret(sec)}, nil
	}
	if tokenExpired(sec.AccessToken, 0) {
		return nil, fmt.Errorf("bps: access token expired and no refresh token available")
	}
	return &auth.Token{AccessToken: sec.AccessToken, RefreshToken: EncodeSecret(sec)}, nil
}

// tokenExpired 判断 access token 是否过期（提前 grace 秒）。
func tokenExpired(accessToken string, grace time.Duration) bool {
	exp := auth.JWTExpiry(accessToken)
	if exp <= 0 {
		return false // 无法解析不判过期
	}
	return time.Now().Unix() >= exp-int64(grace.Seconds())
}
