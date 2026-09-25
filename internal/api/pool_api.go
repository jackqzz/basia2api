package api

import (
	"net/http"

	siteadapter "bps-2api/internal/adapter/bps"
	"bps-2api/internal/secret"
)

// 账号池管理页（bps 版）：全量账号 + 邮箱/账号 ID/套餐（凭据加密存储，此处解密展示）
// + 就绪状态（TokenLocal）+ 测活/批量启停（复用 accounts/actions 任务）。

type poolAccountRow struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	AccountID  string `json:"account_id"`
	Plan       string `json:"plan"`
	HasRefresh bool   `json:"has_refresh"`
	Ready      bool   `json:"ready"`
	Failures   int    `json:"failures"`
	ExpiresAt  int64  `json:"expires_at"`
	Inflight   int    `json:"inflight"`
	Enabled    bool   `json:"enabled"`
}

// handleAdminPoolAccounts 返回账号池明细（含解密后的身份信息）。
func (s *Server) handleAdminPoolAccounts(w http.ResponseWriter, r *http.Request) {
	var failCounts map[string]int
	if s.ka != nil {
		failCounts = s.ka.FailureCounts()
	}
	rows := make([]poolAccountRow, 0, 64)
	for _, acc := range s.pool.Accounts() {
		snap := acc.Snapshot()
		row := poolAccountRow{
			Name:      snap.Name,
			Ready:     snap.LoggedIn,
			Failures:  failCounts[snap.Name],
			ExpiresAt: snap.ExpiresAt,
			Inflight:  snap.Inflight,
			Enabled:   !snap.Disabled,
		}
		if tok := acc.Tokens.Current(); tok != nil {
			row.HasRefresh = tok.RefreshToken != ""
			sec := siteadapter.ParseSecret(secret.Open(tok.RefreshToken))
			if sec.AccessToken == "" {
				sec = siteadapter.ParseSecret(tok.AccessToken)
			}
			row.Email = sec.Email
			row.AccountID = sec.AccountID
			row.Plan = sec.PlanType
			if row.Email == "" && tok.AccessToken != "" {
				row.Email = siteadapter.Email(tok.AccessToken)
			}
			if row.AccountID == "" && tok.AccessToken != "" {
				row.AccountID = siteadapter.AccountID(tok.AccessToken)
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": rows, "total": len(rows)})
}

// handleAdminPoolView 兼容旧链接：账号池已并入 React 控制台，重定向到 /admin/pool。
func (s *Server) handleAdminPoolView(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/pool", http.StatusFound)
}
