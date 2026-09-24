package api

import (
	"net/http"
	"strings"

	siteadapter "bps-2api/internal/adapter/bps"
	"bps-2api/internal/adminapi"
	"bps-2api/internal/pool"
)

// ============================================================
// 机器导入：POST /api/v1/accounts/import（API Key 鉴权）
//
// 供外部注册/采集程序把 ChatGPT 凭据直接推进号池：
//   access_token（必需）、refresh_token（可选，能自动续期）、
//   chatgpt_account_id（可选，JWT 可推导）、email / proxy（可选）
// 导入即可用；access token 过期时由保活用 refresh_token 自动续期。
// ============================================================

type machineImportAccount struct {
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"chatgpt_account_id,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
}

// handleMachineImport 处理机器导入请求。
func (s *Server) handleMachineImport(h *adminapi.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Accounts  []machineImportAccount `json:"accounts"`
			Overwrite bool                   `json:"overwrite"`
		}
		if err := decodeJSON(r, &body); err != nil || len(body.Accounts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "accounts is required"})
			return
		}
		res := adminapi.ImportResult{Errors: []adminapi.ImportItemError{}}
		for _, m := range body.Accounts {
			email := strings.TrimSpace(m.Email)
			access := strings.TrimSpace(m.AccessToken)
			refresh := strings.TrimSpace(m.RefreshToken)
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = pool.SanitizeName(email)
			}
			if name == "" {
				name = "acct-" + shortID(access)
			}
			if access == "" && refresh == "" {
				res.Errors = append(res.Errors, adminapi.ImportItemError{Name: name, Error: "access_token or refresh_token is required"})
				continue
			}
			bundle := siteadapter.BuildImportBundle(access, refresh, m.AccountID, email, strings.TrimSpace(m.Proxy))
			in := adminapi.AccountImport{
				Name:         name,
				Email:        email,
				AccessToken:  access,
				RefreshToken: bundle,
			}
			if p := strings.TrimSpace(m.Proxy); p != "" {
				in.ProxyURL = &p
			}
			enabled := true
			in.Enabled = &enabled
			imported, skipped, err := h.ImportOne(in, body.Overwrite)
			if err != nil {
				res.Errors = append(res.Errors, adminapi.ImportItemError{Name: name, Error: err.Error()})
				continue
			}
			if imported {
				res.Imported++
			} else if skipped {
				res.Skipped++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"imported": res.Imported, "skipped": res.Skipped, "errors": res.Errors})
	}
}
