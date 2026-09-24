package bps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bps-2api/internal/auth"
)

// ============================================================
// ChatGPT OAuth 刷新：POST https://auth.openai.com/oauth/token（form 表单）。
// 与官方 Codex CLI / sub2api 的实现对齐：
//   grant_type=refresh_token, client_id=app_EMoamEEZ73f0CkXaXp7hrann, scope=openid profile email
// 刷新成功后 refresh token 会轮换，必须把新值写回账号池。
// ============================================================

// authBaseURL 是 OAuth 端点基址；单测可覆盖。
var authBaseURL = AuthBaseURL

// RefreshResult 是一次刷新得到的令牌组。
type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
}

// RefreshAccessToken 用 refresh token 换新的 access token。
func RefreshAccessToken(ctx context.Context, refreshToken string, client *http.Client) (*RefreshResult, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, fmt.Errorf("bps: refresh token is empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", OAuthClientID)
	form.Set("refresh_token", rt)
	form.Set("scope", RefreshScope)

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(700 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL+OAuthTokenPath, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("bps: refresh token: %w", err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			var out struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int64  `json:"expires_in"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				return nil, fmt.Errorf("bps: decode refresh response: %w", err)
			}
			if strings.TrimSpace(out.AccessToken) == "" {
				return nil, fmt.Errorf("bps: refresh response missing access_token")
			}
			newRT := strings.TrimSpace(out.RefreshToken)
			if newRT == "" {
				newRT = rt // 部分响应不轮换 refresh token
			}
			return &RefreshResult{AccessToken: out.AccessToken, RefreshToken: newRT, ExpiresIn: out.ExpiresIn}, nil
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest:
			// invalid_grant / 凭据吊销：按无效凭据处理，交由内核摘号。
			return nil, fmt.Errorf("%w: bps refresh rejected: %s", auth.ErrInvalidAPIKey, truncate(string(body), 200))
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = &upstreamError{Status: resp.StatusCode, Msg: "refresh token: " + truncate(string(body), 200)}
			continue
		default:
			return nil, &upstreamError{Status: resp.StatusCode, Msg: "refresh token: " + truncate(string(body), 200)}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("bps: refresh token failed")
	}
	return nil, lastErr
}

// truncate 截断日志/错误里的响应正文。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
