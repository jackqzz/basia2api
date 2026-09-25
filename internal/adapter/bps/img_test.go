package bps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bps-2api/internal/adapter"
)

// input_image：http(s) URL 原样透传给上游抓取。
func TestUserMessageRemoteImageURLPassthrough(t *testing.T) {
	m := adapter.ChatMessage{Role: "user", Content: "look", Files: []adapter.File{{
		Name: "shot.png", Mime: "", URL: "https://example.com/shot.png",
	}}}
	it := userMessageItem(m)
	parts := it["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("want 2 parts, got %v", parts)
	}
	p := parts[1].(map[string]any)
	if p["type"] != "input_image" || p["image_url"] != "https://example.com/shot.png" {
		t.Fatalf("unexpected part: %v", p)
	}
}

// input_image：base64 图 + 公网基址 → 暂存并引用 /v1/img/{id}。
func TestUserMessageDataImageStashedWhenPublicBase(t *testing.T) {
	SetPublicImageBase("https://gw.example.com/")
	defer SetPublicImageBase("")
	m := adapter.ChatMessage{Role: "user", Content: "look", Files: []adapter.File{{
		Name: "s.png", Mime: "image/png", Data: []byte("fakepng"),
	}}}
	it := userMessageItem(m)
	p := it["content"].([]any)[1].(map[string]any)
	url, _ := p["image_url"].(string)
	if p["type"] != "input_image" || !strings.HasPrefix(url, "https://gw.example.com/v1/img/img_") {
		t.Fatalf("unexpected part: %v", p)
	}
	// 暂存项能取回
	id := strings.TrimPrefix(url, "https://gw.example.com/v1/img/")
	imgStash.Lock()
	_, ok := imgStash.items[id]
	imgStash.Unlock()
	if !ok {
		t.Fatal("stashed image not found")
	}
}

// input_image：base64 图无公网基址 → 占位文本降级，不生成 data: URL。
func TestUserMessageDataImagePlaceholderWithoutPublicBase(t *testing.T) {
	SetPublicImageBase("")
	m := adapter.ChatMessage{Role: "user", Content: "look", Files: []adapter.File{{
		Name: "s.png", Mime: "image/png", Data: []byte("fakepng"),
	}}}
	it := userMessageItem(m)
	p := it["content"].([]any)[1].(map[string]any)
	if p["type"] != "input_text" || !strings.Contains(p["text"].(string), "content omitted") {
		t.Fatalf("unexpected part: %v", p)
	}
	if strings.Contains(p["text"].(string), "data:") {
		t.Fatal("data: URL leaked into placeholder")
	}
}

// 本地图上传：三步文件通道换 SAS URL，按内容哈希缓存复用。
func TestPrepareImagesUploadsAndCaches(t *testing.T) {
	var initN, putN, cbN atomic.Int32
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			initN.Add(1)
			w.Write([]byte(`{"upload_url":"` + ts.URL + `/sas","file_id":"file_x"}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/sas"):
			putN.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/uploaded"):
			cbN.Add(1)
			w.Write([]byte(`{"download_url":"https://oai/dl-sas"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	old := filesAPIBase
	filesAPIBase = ts.URL
	defer func() { filesAPIBase = old }()

	// upload_url 里嵌 ts.URL/sas：init 响应要动态写，改写成可替换
	c := &httpClient{http: ts.Client()}
	sec := Secret{AccessToken: "at", AccountID: "acct-1"}
	nr := &adapter.NativeRequest{Messages: []adapter.ChatMessage{{Role: "user",
		Files: []adapter.File{{Name: "a.png", Mime: "image/png", Data: []byte("img-bytes")}}}}}

	c.prepareImages(context.Background(), sec, nr)
	if nr.Messages[0].Files[0].URL != "https://oai/dl-sas" {
		t.Fatalf("url = %q", nr.Messages[0].Files[0].URL)
	}
	if putN.Load() != 1 || cbN.Load() != 1 {
		t.Fatalf("put=%d cb=%d", putN.Load(), cbN.Load())
	}
	// 相同内容第二次：缓存命中，不再上传
	nr2 := &adapter.NativeRequest{Messages: []adapter.ChatMessage{{Role: "user",
		Files: []adapter.File{{Mime: "image/png", Data: []byte("img-bytes")}}}}}
	c.prepareImages(context.Background(), sec, nr2)
	if nr2.Messages[0].Files[0].URL != "https://oai/dl-sas" || initN.Load() != 1 {
		t.Fatalf("cache miss: url=%q init=%d", nr2.Messages[0].Files[0].URL, initN.Load())
	}
}

// SAS se 解析：缓存 TTL 必须按签名过期时间算（实测 ~1h），不是长缓存。
func TestSASExpiryParsedFromURL(t *testing.T) {
	u := "https://x.oaiusercontent.com/files/a/raw?se=2026-09-25T09%3A42%3A58Z&sp=r&sv=2026-02-06"
	exp := sasExpiry(u)
	want, _ := time.Parse(time.RFC3339, "2026-09-25T09:37:58Z") // se-5min
	if !exp.Equal(want) {
		t.Fatalf("sasExpiry = %v, want %v", exp, want)
	}
	if d := sasExpiry("https://x/no-sas").Sub(time.Now()); d > 46*time.Minute || d < 40*time.Minute {
		t.Fatalf("fallback expiry = %v", d)
	}
}

// 跨账号/过期 URL：File 带的 managed URL 不在本账号缓存里 → 按当前账号重传。
func TestPrepareImagesReuploadsForeignManagedURL(t *testing.T) {
	var putN atomic.Int32
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			w.Write([]byte(`{"upload_url":"` + ts.URL + `/sas","file_id":"file_y"}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/sas"):
			putN.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/uploaded"):
			w.Write([]byte(`{"download_url":"https://oai/new-sas"}`))
		}
	}))
	defer ts.Close()
	old := filesAPIBase
	filesAPIBase = ts.URL
	defer func() { filesAPIBase = old }()

	c := &httpClient{http: ts.Client()}
	sec := Secret{AccessToken: "at", AccountID: "acct-B"}
	// URL 是别账号产的 SAS，缓存里查不到 → 必须重传并覆盖
	nr := &adapter.NativeRequest{Messages: []adapter.ChatMessage{{Role: "user",
		Files: []adapter.File{{Mime: "image/png", Data: []byte("foreign-img"),
			URL: "https://sdmntprwestus3.oaiusercontent.com/files/old/raw?se=expired"}}}}}

	c.prepareImages(context.Background(), sec, nr)
	if nr.Messages[0].Files[0].URL != "https://oai/new-sas" || putN.Load() != 1 {
		t.Fatalf("foreign url not reuploaded: %q put=%d", nr.Messages[0].Files[0].URL, putN.Load())
	}
	// 客户端自己的公网 URL 不动（且它有 Data 的场景）
	nr2 := &adapter.NativeRequest{Messages: []adapter.ChatMessage{{Role: "user",
		Files: []adapter.File{{Mime: "image/png", Data: []byte("x"),
			URL: "https://cdn.example.com/img.png"}}}}}
	c.prepareImages(context.Background(), sec, nr2)
	if nr2.Messages[0].Files[0].URL != "https://cdn.example.com/img.png" {
		t.Fatalf("client url overwritten: %q", nr2.Messages[0].Files[0].URL)
	}
}
