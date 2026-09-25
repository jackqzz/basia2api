package bps

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 上游 input_image 只认 http(s) URL 并服务端代抓（data: 一律 422）。
// 这里给网关一个自托管暂存：客户端 base64 图 → /v1/img/{id}，让上游来抓。
// URL id 随机不可猜 → 免鉴权暴露安全（上游抓取不带 Authorization）。
//
// 仅当配置了 PublicBaseURL（网关的公网可达地址）才启用；否则 data: 图
// 仍降级为占位文本（见 map.go userMessageItem）。

const (
	imgStashTTL      = 15 * time.Minute // 上游在建连后几秒内抓取，15 分钟足够
	imgStashMaxItems = 512
	imgStashMaxBytes = 96 << 20 // 96MB 总暂存上限
	imgStashMaxFile  = 16 << 20 // 单图上限（data: URL 膨胀前的原始字节）
)

type stashedImage struct {
	data []byte
	mime string
	at   time.Time
}

var imgStash = struct {
	sync.Mutex
	items map[string]stashedImage
	size  int
}{items: map[string]stashedImage{}}

// publicImageBase 是网关公网基址（e.g. https://gw.example.com），空 = 未配置。
var publicImageBase string

// SetPublicImageBase 设置网关公网基址；空串关闭 data: 图自托管（回落占位文本）。
func SetPublicImageBase(base string) {
	publicImageBase = strings.TrimRight(strings.TrimSpace(base), "/")
}

// stashImage 暂存图片字节并返回公网 URL；超限时返回空串（调用方回落占位）。
func stashImage(data []byte, mime string) string {
	if publicImageBase == "" || len(data) == 0 || len(data) > imgStashMaxFile {
		return ""
	}
	imgStash.Lock()
	defer imgStash.Unlock()
	if len(imgStash.items) >= imgStashMaxItems || imgStash.size+len(data) > imgStashMaxBytes {
		// 满：先扫一遍过期项，还满就拒（占位降级）。
		evictExpiredLocked()
		if len(imgStash.items) >= imgStashMaxItems || imgStash.size+len(data) > imgStashMaxBytes {
			return ""
		}
	}
	id := "img_" + newID() + newID()
	imgStash.items[id] = stashedImage{data: data, mime: mime, at: time.Now()}
	imgStash.size += len(data)
	return publicImageBase + "/v1/img/" + id
}

// evictExpiredLocked 清掉过期项（调用方需持锁）。
func evictExpiredLocked() {
	cut := time.Now().Add(-imgStashTTL)
	for id, im := range imgStash.items {
		if im.at.Before(cut) {
			imgStash.size -= len(im.data)
			delete(imgStash.items, id)
		}
	}
}

// ServeStashedImage 挂载在 /v1/img/{id}：给上游图片抓取服务用，免鉴权。
// id 随机（img_+32hex）不可枚举。
func ServeStashedImage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/img/")
	imgStash.Lock()
	im, ok := imgStash.items[id]
	imgStash.Unlock()
	if !ok || time.Since(im.at) > imgStashTTL {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", im.mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Length", fmt.Sprint(len(im.data)))
	_, _ = w.Write(im.data)
}
