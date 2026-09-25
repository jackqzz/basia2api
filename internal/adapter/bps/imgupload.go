package bps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bps-2api/internal/adapter"
)

// 上游 input_image 只收 http(s) URL（data: 一律 422）。本地图走 ChatGPT
// 官方文件通道上传拿 oaiusercontent 匿名 SAS 下载地址——上游抓取服务就在
// OpenAI 内网，抓自家 CDN 不需要网关公网可达：
//
//	POST {website}/backend-api/files            → upload_url + file_id
//	PUT  upload_url (x-ms-blob-type:BlockBlob)  → 201
//	POST {website}/backend-api/files/{id}/uploaded → download_url（~24h 有效）
//
// 关键点：SAS PUT 过 Cloudflare，UA 必须是客户端画像（Python/curl 默认 UA 403）。
// 可变变量：单测用 httptest 顶替。
var filesAPIBase = "https://chatgpt.com/backend-api"

// imgURLCache 按 (account, sha256(data)) 缓存 SAS URL：对话历史每轮重放
// 同一张图，不缓存会每轮重新上传。注意 SAS download_url 实测只有 ~1h 有效
// （se 参数是签名过期时间），TTL 必须按 se 算，不能拍脑袋长缓存——
// 缓存过期 URL 重放会被上游 fetch 403。
var imgURLCache = struct {
	sync.Mutex
	items map[string]imgCacheEntry
}{items: map[string]imgCacheEntry{}}

type imgCacheEntry struct {
	url string
	exp time.Time
}

// managedImageURL 判断 URL 是不是我们自己产出的（oaiusercontent SAS / 网关暂存）：
// 重试换账号时这些 URL 属于别的账号或已过期，需要按当前账号重传而不是复用。
func managedImageURL(u string) bool {
	return strings.Contains(u, "oaiusercontent.com/files/") ||
		strings.Contains(u, "/v1/img/")
}

// sasExpiry 从 SAS URL 里解 se（signature expiry）；拿不到给 45min 兜底。
func sasExpiry(rawURL string) time.Time {
	if q, err := url.Parse(rawURL); err == nil {
		if se := q.Query().Get("se"); se != "" {
			if t, err := time.Parse(time.RFC3339, se); err == nil {
				return t.Add(-5 * time.Minute) // 留余量
			}
		}
	}
	return time.Now().Add(45 * time.Minute)
}

// uploadDataImage 上传一张本地图，返回 BPS 可抓的 SAS 下载地址。
func (c *httpClient) uploadDataImage(ctx context.Context, sec Secret, data []byte, mime, name string) (string, error) {
	sum := sha256.Sum256(data)
	key := sec.AccountID + "/" + hex.EncodeToString(sum[:])
	imgURLCache.Lock()
	if e, ok := imgURLCache.items[key]; ok && time.Now().Before(e.exp) {
		imgURLCache.Unlock()
		return e.url, nil
	}
	imgURLCache.Unlock()

	url, err := c.uploadDataImageCold(ctx, sec, data, mime, name)
	if err != nil {
		return "", err
	}
	exp := sasExpiry(url)
	imgURLCache.Lock()
	imgURLCache.items[key] = imgCacheEntry{url: url, exp: exp}
	// 粗放上限：满了整体重建（缓存只是性能优化，丢了重传而已）。
	if len(imgURLCache.items) > 512 {
		imgURLCache.items = map[string]imgCacheEntry{key: {url: url, exp: exp}}
	}
	imgURLCache.Unlock()
	return url, nil
}

// refreshImages 上游报 "Error while downloading file" 时自救：清掉本请求里
// 所有我们产出的 URL（SAS 过期或跨账号复用）及其缓存项，按当前账号重新上传。
// 返回是否真的动过（有没有图值得重试）。
func (c *httpClient) refreshImages(ctx context.Context, sec Secret, nr *adapter.NativeRequest) bool {
	touched := false
	for i := range nr.Messages {
		for j := range nr.Messages[i].Files {
			f := &nr.Messages[i].Files[j]
			if len(f.Data) == 0 || !managedImageURL(f.URL) {
				continue
			}
			sum := sha256.Sum256(f.Data)
			imgURLCache.Lock()
			delete(imgURLCache.items, sec.AccountID+"/"+hex.EncodeToString(sum[:]))
			imgURLCache.Unlock()
			f.URL = ""
			touched = true
		}
	}
	if !touched {
		return false
	}
	c.prepareImages(ctx, sec, nr)
	return true
}

func (c *httpClient) uploadDataImageCold(ctx context.Context, sec Secret, data []byte, mime, name string) (string, error) {
	if mime == "" {
		mime = "image/png"
	}
	if name == "" {
		name = "image." + strings.TrimPrefix(mime, "image/")
	}
	// 1) 申请上传位
	initBody, _ := json.Marshal(map[string]any{
		"file_name": name, "use_case": "my_files",
		"mime_type": mime, "file_size_bytes": len(data),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, filesAPIBase+"/files", bytes.NewReader(initBody))
	if err != nil {
		return "", err
	}
	c.authHeaders(req, sec)
	var init struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if err := c.doJSON(req, &init); err != nil {
		return "", fmt.Errorf("files init: %w", err)
	}
	if init.UploadURL == "" || init.FileID == "" {
		return "", fmt.Errorf("files init: missing upload_url/file_id")
	}
	// 2) SAS PUT（必须带 x-ms-blob-type + 客户端 UA，否则 Cloudflare 403）
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, init.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mime)
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClientRef().Do(req)
	if err != nil {
		return "", fmt.Errorf("sas put: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("sas put: status %d", resp.StatusCode)
	}
	// 3) uploaded 回调 → download_url
	req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		filesAPIBase+"/files/"+init.FileID+"/uploaded",
		strings.NewReader(`{"file_id":"`+init.FileID+`"}`))
	if err != nil {
		return "", err
	}
	c.authHeaders(req, sec)
	var done struct {
		DownloadURL string `json:"download_url"`
	}
	if err := c.doJSON(req, &done); err != nil {
		return "", fmt.Errorf("files uploaded: %w", err)
	}
	if done.DownloadURL == "" {
		return "", fmt.Errorf("files uploaded: missing download_url")
	}
	return done.DownloadURL, nil
}

// authHeaders 打 chatgpt.com backend-api 的鉴权头组。
func (c *httpClient) authHeaders(req *http.Request, sec Secret) {
	req.Header.Set("Authorization", "Bearer "+sec.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
}

// doJSON 发请求并把 2xx 响应体解进 out；非 2xx 收进错误。
func (c *httpClient) doJSON(req *http.Request, out any) error {
	resp, err := c.httpClientRef().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	return json.Unmarshal(raw, out)
}

// prepareImages 把消息里的本地图（Data 载荷）上传到文件通道并回填 URL；
// 失败只记日志、留空 → map.go 回落占位文本，不带崩整轮请求。
//
// f.URL 已带值时分两种：客户端自己给的公网 URL 原样不动；我们自己的
// SAS/暂存 URL（managedImageURL）若不属于当前账号缓存（重试换账号时）
// 则按当前账号重传，避免上游 fetch 403。
func (c *httpClient) prepareImages(ctx context.Context, sec Secret, nr *adapter.NativeRequest) {
	for i := range nr.Messages {
		for j := range nr.Messages[i].Files {
			f := &nr.Messages[i].Files[j]
			if len(f.Data) == 0 || !strings.HasPrefix(strings.ToLower(f.Mime), "image/") {
				continue
			}
			if f.URL != "" {
				if !managedImageURL(f.URL) {
					continue // 客户端公网 URL：透传
				}
				// 我们产出的 URL：命中本账号缓存才算有效，否则按当前账号重传。
				sum := sha256.Sum256(f.Data)
				imgURLCache.Lock()
				e, ok := imgURLCache.items[sec.AccountID+"/"+hex.EncodeToString(sum[:])]
				imgURLCache.Unlock()
				if ok && time.Now().Before(e.exp) && e.url == f.URL {
					continue
				}
			}
			url, err := c.uploadDataImage(ctx, sec, f.Data, f.Mime, f.Name)
			if err != nil {
				log.Printf("bps: image upload failed (%s, %d bytes): %v — placeholder fallback", f.Name, len(f.Data), err)
				f.URL = "" // 清掉别的账号的旧 URL，让 map.go 走占位降级
				continue
			}
			f.URL = url
		}
	}
}
