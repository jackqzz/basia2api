# ACCEPTANCE — bps 适配

执行时间：2026-09-24。环境：Windows 本机、Go 1.26.5、无 PostgreSQL、真实 ChatGPT 账号凭据。

## 验收项与证据

| # | 验收项 | 结果 | 证据 |
|---|---|---|---|
| 1 | `go build ./...` | ✅ | 退出码 0 |
| 2 | `go test ./...` | ✅ | 全部 `ok`（23 个包；修掉 2 个基线问题：Windows 时钟粒度导致 MRU 测试抖动、默认提示词改 off 后测试同步） |
| 3 | 账号导入（access + refresh + account_id） | ✅ | `POST /api/v1/accounts/import` → `{"imported":1}`；`/v1/accounts` 显示 `logged_in:true`、email、到期时间 |
| 4 | `GET /v1/models` | ✅ | 返回 5 个模型，`owned_by:"bps"` |
| 5 | `/v1/chat/completions` 非流式 | ✅ | 中文问“中国的首都是哪里？” → `中国的首都是北京。`（UTF-8 正确），usage 17577/9 |
| 6 | `/v1/chat/completions` 流式 | ✅ | SSE content delta `Paris`，`finish_reason:"stop"`，带 usage 帧 |
| 7 | `/v1/responses` | ✅ | 事件序列：`response.created` → `output_item.added` → `output_text.delta` → `output_text.done` → `response.completed` |
| 8 | `/v1/messages` | ✅ | 事件序列：`message_start` → `content_block_start` → `text_delta` → `content_block_stop` → `message_delta` → `message_stop` |
| 9 | refresh_token 自动续期 | ✅ | 仅导入 refresh_token 的账号：保活 `keepalive: refresh "taylor-rt" ok`，50s 后 `logged_in:true`，可用该账号完成对话 |
| 10 | 无 PostgreSQL 运行 | ✅ | 启动日志 `no DATABASE_URL: using JSON file store at ...`，重启后账号自动加载（`account "taylor" loaded`） |
| 11 | 管理后台静态资源 | ✅ | `web` 构建成功（`npm run build` → `web/dist`），Dockerfile 会打进镜像 |
| 12 | 文档产出 | ✅ | `docs/bps适配/` 六份文档 + `README.md` + `项目文件功能说明.md` |

## 真机端到端记录（节选）

```
=== 1. import taylor (access + refresh) ===
{"errors":[],"imported":1,"skipped":0}

=== 2. chat/completions non-stream (UTF-8 Chinese) ===
reply: 中国的首都是北京。
finish_reason=stop model=gpt-5.6-sol prompt=17577 completion=9

=== 3. chat/completions stream ===
data: {"choices":[{"index":0,"delta":{"content":"Paris"}}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

=== 4. /v1/responses events ===
event: response.created ... event: response.completed

=== 5. /v1/messages events ===
event: message_start ... event: message_stop

=== 6. import refresh-only account ===
keepalive: refresh "taylor-rt" ok
taylor-rt ready=True waited=50s
```

## 未覆盖 / 已知限制

- 多轮会话（`previous_response_id` / 会话关联）未验证：当前每次请求独立生成 `task_id`/`turn_id`。
- 上游额度百分比未接（`FetchUsage` 只用 JWT 身份字段）。
- 图片输入路径已实现（data URL `input_image`）但未真机验证。
- 账号池并发/限流沿用内核默认，未压测。
