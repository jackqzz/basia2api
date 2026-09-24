package bps

import (
	"context"
	"net/http"
	"time"

	"bps-2api/internal/adapter"
	"bps-2api/internal/egress"
)

// mockClient 是进程内假上游，供 -mock 模式和单测使用。
type mockClient struct{}

// newMockClient 构造 mock 客户端。
func newMockClient() *mockClient { return &mockClient{} }

// Stream 吐固定文本与一次用量事件。
func (c *mockClient) Stream(ctx context.Context, nr *adapter.NativeRequest, emit func(adapter.Event) bool) error {
	if !emit(adapter.Event{Text: "Hello from " + DisplayName + " mock.\n"}) {
		return nil
	}
	emit(adapter.Event{
		Ended:        true,
		InputTokens:  4,
		OutputTokens: 8,
		ContextWindow: &adapter.ContextWindowStatus{
			TokensUsed: 4,
		},
	})
	return nil
}

// ListModels 返回静态目录。
func (c *mockClient) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	out := make([]adapter.ModelInfo, 0, len(staticModels()))
	for _, m := range staticModels() {
		out = append(out, *m)
	}
	return out, nil
}

// FetchUsage 返回假用量快照。
func (c *mockClient) FetchUsage(ctx context.Context, websiteURL string) (adapter.UsageSnapshot, error) {
	return adapter.UsageSnapshot{Email: "mock@example.com", FetchedAt: time.Now().Unix()}, nil
}

// SetBaseURL 空实现。
func (c *mockClient) SetBaseURL(url string) {}

// SetEgress 空实现。
func (c *mockClient) SetEgress(s egress.Settings) error { return nil }

// SetHTTPClient 空实现。
func (c *mockClient) SetHTTPClient(h *http.Client) {}
