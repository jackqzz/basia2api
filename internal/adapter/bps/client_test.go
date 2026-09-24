package bps

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bps-2api/internal/adapter"
)

// newTestAccessToken 造一个带 account_id 的合法 access JWT。
func newTestAccessToken(t *testing.T, accountID string) string {
	t.Helper()
	return buildTestJWT(t, map[string]any{
		"exp":                            int64(4102444800), // 2100
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": accountID},
		"https://api.openai.com/profile": map[string]any{"email": "u@example.com"},
	})
}

func TestStreamSendsBpsHeadersAndBody(t *testing.T) {
	type captured struct {
		header http.Header
		body   map[string]any
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- captured{header: r.Header.Clone(), body: body}
		if r.URL.Path != "/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sampleSSE)
	}))
	defer srv.Close()

	access := newTestAccessToken(t, "acct-42")
	client := newHTTPClient(adapter.ClientConfig{
		BaseURL:    srv.URL,
		TokenProvider: func() (string, error) {
			return EncodeSecret(Secret{AccessToken: access, RefreshToken: "rt.1"}), nil
		},
	})
	nr, err := mapChat(adapter.ChatRequest{Model: "gpt-5.6-sol", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("mapChat: %v", err)
	}
	var text strings.Builder
	if err := client.Stream(context.Background(), nr, func(ev adapter.Event) bool {
		text.WriteString(ev.Text)
		return true
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text.String() != "Hello" {
		t.Fatalf("text=%q", text.String())
	}
	c := <-got
	if c.header.Get("Authorization") != "Bearer "+access {
		t.Fatalf("authorization=%q", c.header.Get("Authorization"))
	}
	if c.header.Get("chatgpt-account-id") != "acct-42" || c.header.Get("x-openai-account-id") != "acct-42" {
		t.Fatalf("account headers=%v", c.header)
	}
	if c.header.Get("x-basispoints-auth-mode") != "chatgpt" {
		t.Fatalf("auth mode=%q", c.header.Get("x-basispoints-auth-mode"))
	}
	meta, _ := c.body["metadata"].(map[string]any)
	if meta["task_id"] == "" || meta["turn_id"] == "" {
		t.Fatalf("metadata=%v", c.body["metadata"])
	}
	if c.body["model"] != "gpt-5.6-sol" || c.body["stream"] != true {
		t.Fatalf("body=%v", c.body)
	}
}

func TestStreamHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad token"}}`)
	}))
	defer srv.Close()

	client := newHTTPClient(adapter.ClientConfig{
		BaseURL: srv.URL,
		TokenProvider: func() (string, error) {
			return EncodeSecret(Secret{AccessToken: newTestAccessToken(t, "acct-1")}), nil
		},
	})
	err := client.Stream(context.Background(), &adapter.NativeRequest{Model: "gpt-5.6-sol"}, func(adapter.Event) bool { return true })
	up, ok := err.(*upstreamError)
	if !ok || up.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unexpected err: %#v", err)
	}
}

func TestRefreshAccessToken(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotForm = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-at","refresh_token":"rt.2","expires_in":3600}`)
	}))
	defer srv.Close()

	old := authBaseURL
	authBaseURL = srv.URL
	defer func() { authBaseURL = old }()

	res, err := RefreshAccessToken(context.Background(), "rt.1", srv.Client())
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if res.AccessToken != "new-at" || res.RefreshToken != "rt.2" {
		t.Fatalf("result=%+v", res)
	}
	for _, want := range []string{"grant_type=refresh_token", "client_id=" + OAuthClientID, "refresh_token=rt.1"} {
		if !strings.Contains(gotForm, want) {
			t.Fatalf("form missing %q: %s", want, gotForm)
		}
	}
}

func TestExchangeCredentialRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"`+newTestAccessToken(t, "acct-7")+`","refresh_token":"rt.9"}`)
	}))
	defer srv.Close()

	old := authBaseURL
	authBaseURL = srv.URL
	defer func() { authBaseURL = old }()

	a := New(Config{})
	tok, err := a.ExchangeCredential("", "rt.5", "", "", srv.Client())
	if err != nil {
		t.Fatalf("ExchangeCredential: %v", err)
	}
	sec := ParseSecret(tok.RefreshToken)
	if sec.RefreshToken != "rt.9" || sec.AccountID != "acct-7" {
		t.Fatalf("token bundle=%+v", sec)
	}
}
