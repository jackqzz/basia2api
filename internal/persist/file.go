package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// File 是 JSON 文件目录后端：<dir>/<kind>/<id>.json。
// 单机部署（无 PostgreSQL）时的持久化实现，语义对齐 Memory（缺失 = nil, nil）。
type File struct {
	mu  sync.Mutex
	dir string
}

// NewFile 创建文件后端；目录不存在时首次写入自动创建。
func NewFile(dir string) *File {
	return &File{dir: dir}
}

// safeName 把 kind/id 归一化成安全文件名（防目录穿越）。
func safeName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	out := strings.Trim(sb.String(), ".")
	if out == "" {
		out = "_"
	}
	return out
}

// docPath 返回文档的磁盘路径。
func (f *File) docPath(kind, id string) string {
	return filepath.Join(f.dir, safeName(kind), safeName(id)+".json")
}

// LoadDoc 读取文档；不存在返回 (nil, nil)。
func (f *File) LoadDoc(kind, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.docPath(kind, id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// SaveDoc 原子写入文档（临时文件 + rename）。
func (f *File) SaveDoc(kind, id string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.docPath(kind, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteDoc 删除文档；不存在视为成功。
func (f *File) DeleteDoc(kind, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := os.Remove(f.docPath(kind, id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListDocs 列出某 kind 下的全部文档（id → payload）。
func (f *File) ListDocs(kind string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Join(f.dir, safeName(kind))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make(map[string][]byte, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", kind, name, err)
		}
		out[strings.TrimSuffix(name, ".json")] = raw
	}
	return out, nil
}
