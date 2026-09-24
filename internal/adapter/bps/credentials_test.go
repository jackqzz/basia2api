package bps

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// buildTestJWT 拼一个只含 payload 的假 JWT（签名不参与解析）。
func buildTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func TestParseSecretAuthJSON(t *testing.T) {
	access := buildTestJWT(t, map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_plan_type": "self_serve_business_prolite"},
		"https://api.openai.com/profile": map[string]any{"email": "a@b.com"},
	})
	raw := `{"access_token":"` + access + `","refresh_token":"rt.1.abc","chatgpt_account_id":"acct-1","email":"a@b.com"}`
	sec := ParseSecret(raw)
	if sec.AccessToken != access || sec.RefreshToken != "rt.1.abc" {
		t.Fatalf("unexpected secret: %+v", sec)
	}
	if sec.AccountID != "acct-1" || sec.Email != "a@b.com" {
		t.Fatalf("unexpected identity: %+v", sec)
	}
}

func TestParseSecretRawForms(t *testing.T) {
	access := buildTestJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"},
	})
	if got := ParseSecret(access); got.AccessToken != access || got.AccountID != "acct-2" {
		t.Fatalf("raw jwt: %+v", got)
	}
	if got := ParseSecret("rt.1.xyz"); got.RefreshToken != "rt.1.xyz" || got.AccessToken != "" {
		t.Fatalf("raw rt: %+v", got)
	}
	sec := ParseSecret(EncodeSecret(Secret{RefreshToken: "rt.9", AccountID: "acct-9", Email: "x@y.com"}))
	if sec.RefreshToken != "rt.9" || sec.AccountID != "acct-9" || sec.Email != "x@y.com" {
		t.Fatalf("bundle roundtrip: %+v", sec)
	}
}

func TestParseSecretCookieForm(t *testing.T) {
	sec := ParseSecret("access_token=eyJx.eyJy.zzz; refresh_token=rt.2.def")
	if !strings.HasPrefix(sec.AccessToken, "eyJx") || sec.RefreshToken != "rt.2.def" {
		t.Fatalf("cookie form: %+v", sec)
	}
}

func TestBuildImportBundleFillsFromJWT(t *testing.T) {
	access := buildTestJWT(t, map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-3", "chatgpt_plan_type": "plus"},
		"https://api.openai.com/profile": map[string]any{"email": "c@d.com"},
	})
	bundle := BuildImportBundle(access, "rt.3", "", "", "")
	sec := ParseSecret(bundle)
	if sec.AccountID != "acct-3" || sec.Email != "c@d.com" || sec.PlanType != "plus" {
		t.Fatalf("bundle identity: %+v", sec)
	}
}

func TestNewIDUnique(t *testing.T) {
	a, b := newID(), newID()
	if a == b || len(a) != 32 {
		t.Fatalf("newID not unique/expected len: %q %q", a, b)
	}
}
