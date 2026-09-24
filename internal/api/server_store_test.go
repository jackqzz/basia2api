package api

import (
	"testing"

	"bps-2api/internal/config"
)

// 无 PostgreSQL 且非 mock 时：退化为本地 JSON 文件存储（单机模式），不再报错。
func TestNewServerFallsBackToFileStore(t *testing.T) {
	srv, err := NewServer(&config.Config{
		CredentialDir: t.TempDir(),
		MockMode:      false,
	})
	if err != nil {
		t.Fatalf("want file-store fallback, got %v", err)
	}
	srv.Shutdown()
}
