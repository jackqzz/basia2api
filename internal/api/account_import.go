package api

import (
	"encoding/csv"
	"strings"

	"bps-2api/internal/accountfile"
	siteadapter "bps-2api/internal/adapter/bps"
	"bps-2api/internal/adminapi"
)

// ============================================================
// CSV 导入（bps/ChatGPT 凭据）→ 号池账号
//
// 支持两种列布局（表头归一化匹配，认不出按位置）：
//   email,access_token,refresh_token,chatgpt_account_id,proxy
// 其中 access_token 是 ChatGPT JWT，refresh_token 是 rt.1... 刷新令牌（可缺省）。
// account_id 缺省时由 access_token 的 JWT claim 推导。
// ============================================================

// looksLikeCredentialCSV 判断导入文本是不是 CSV（不是纯 token 行）。
func looksLikeCredentialCSV(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		return false
	}
	first := firstNonEmptyLine(text)
	if first == "" {
		return false
	}
	if strings.Contains(first, "----") {
		return false // 老格式交给内核解析器
	}
	if !strings.Contains(first, ",") {
		return false
	}
	rec, err := csv.NewReader(strings.NewReader(first)).Read()
	if err != nil {
		return false
	}
	return looksLikeBpsHeader(rec) || accountfile.LooksLikeHeader(rec)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")); s != "" && !strings.HasPrefix(s, "#") {
			return s
		}
	}
	return ""
}

// normalizeHeader 归一化表头：小写去空格/下划线/连字符/点。
func normalizeHeader(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))) {
		switch r {
		case ' ', '_', '-', '.', '*', '\t', '\u00a0':
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// headerAliases 是各列识别的别名集合。
var (
	emailAliases   = []string{"email", "mail", "account", "账号", "邮箱"}
	accessAliases  = []string{"accesstoken", "access", "token", "at", "jwt", "访问令牌"}
	refreshAliases = []string{"refreshtoken", "refresh", "rt", "刷新令牌"}
	acctAliases    = []string{"chatgptaccountid", "accountid", "acctid", "account_id", "账号id"}
	proxyAliases   = []string{"proxy", "proxyurl", "httpproxy", "代理", "出口"}
)

func matchHeader(h string, aliases []string) bool {
	if h == "" {
		return false
	}
	for _, a := range aliases {
		if h == normalizeHeader(a) || strings.HasPrefix(h, normalizeHeader(a)) {
			return true
		}
	}
	return false
}

// looksLikeBpsHeader 判断一行是不是 bps 凭据表头（至少含 access_token 或 refresh_token 列）。
func looksLikeBpsHeader(rec []string) bool {
	hasAccess, hasRefresh := false, false
	for _, c := range rec {
		h := normalizeHeader(c)
		if matchHeader(h, accessAliases) {
			hasAccess = true
		}
		if matchHeader(h, refreshAliases) {
			hasRefresh = true
		}
	}
	return hasAccess || hasRefresh
}

// headerColumns 定位各列下标；-1 表示没有该列。
func headerColumns(rec []string) (email, access, refresh, acct, proxy int) {
	email, access, refresh, acct, proxy = -1, -1, -1, -1, -1
	for i, c := range rec {
		h := normalizeHeader(c)
		switch {
		case email < 0 && matchHeader(h, emailAliases):
			email = i
		case access < 0 && matchHeader(h, accessAliases):
			access = i
		case refresh < 0 && matchHeader(h, refreshAliases):
			refresh = i
		case acct < 0 && matchHeader(h, acctAliases):
			acct = i
		case proxy < 0 && matchHeader(h, proxyAliases):
			proxy = i
		}
	}
	return
}

// credentialCSVToImports 把 CSV 文本转成账号导入条目（解析不了返回 ok=false）。
func credentialCSVToImports(text, namePrefix string) ([]adminapi.AccountImport, bool) {
	recs, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, false
	}
	colEmail, colAccess, colRefresh, colAcct, colProxy := -1, -1, -1, -1, -1
	start := 0
	if looksLikeBpsHeader(recs[0]) || accountfile.LooksLikeHeader(recs[0]) {
		colEmail, colAccess, colRefresh, colAcct, colProxy = headerColumns(recs[0])
		start = 1
	} else {
		// 无表头：按位置 email,access_token,refresh_token,account_id,proxy
		colEmail, colAccess, colRefresh, colAcct, colProxy = 0, 1, 2, 3, 4
	}
	out := make([]adminapi.AccountImport, 0, len(recs))
	seen := map[string]bool{}
	for i := start; i < len(recs); i++ {
		rec := recs[i]
		email := cellAt(rec, colEmail)
		access := cellAt(rec, colAccess)
		refresh := cellAt(rec, colRefresh)
		acct := cellAt(rec, colAcct)
		proxy := cellAt(rec, colProxy)
		if access == "" && refresh == "" {
			continue
		}
		key := firstNonEmptyStr(email, access)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		name := strings.TrimSpace(namePrefix)
		if email != "" {
			name = accountfile.NameFor(namePrefix, email)
		} else if name == "" {
			name = "acct-" + shortID(access)
		}
		in := adminapi.AccountImport{
			Name:         name,
			Email:        email,
			AccessToken:  access,
			RefreshToken: siteadapter.BuildImportBundle(access, refresh, acct, email, proxy),
		}
		if proxy != "" {
			p := proxy
			in.ProxyURL = &p
		}
		out = append(out, in)
	}
	return out, len(out) > 0
}

// cellAt 取行内第 i 列（越界返回空）。
func cellAt(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// shortID 取 token 前 6 位做账号名后缀。
func shortID(token string) string {
	t := strings.TrimSpace(token)
	if len(t) > 6 {
		t = t[:6]
	}
	if t == "" {
		return "unknown"
	}
	return t
}
