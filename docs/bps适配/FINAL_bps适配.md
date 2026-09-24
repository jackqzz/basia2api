# FINAL — bps 适配交付总结

## 交付物

| 交付物 | 位置 |
|---|---|
| 反代项目 | `D:\niubiai.cc\bps2api`（Go，module `bps-2api`） |
| 上游适配器 | `internal/adapter/bps/`（10 个源文件 + 4 个测试文件） |
| 文件存储后端 | `internal/persist/file.go` |
| 文档 | `docs/bps适配/`（对齐/设计/任务/验收/总结/待办）、`README.md`、`项目文件功能说明.md` |

## 实现摘要

1. **协议转换**：客户端 OpenAI/Anthropic 请求 → 内核统一 `ChatRequest` → bps Responses 请求体；上游 SSE → `adapter.Event` → 各协议帧由内核生成。
2. **认证与续期**：账号 = ChatGPT `access_token` + `refresh_token`；`chatgpt-account-id` 从 JWT 推导；`auth.openai.com/oauth/token` form 表单刷新，refresh token 轮换后回写号池；保活 60s 扫描、过期前 15 分钟预刷新。
3. **导入链路**：JSON / CSV / `----` 行 / 机器导入 API，全部落到统一的 `auth.Token{AccessToken, RefreshToken(bundle)}`。
4. **内核解耦**：删除 prism 适配器与浏览器登录 sidecar；4 个编译期耦合文件改为 bps 语义；品牌/配置/部署文件同步。
5. **单机可用**：无 PostgreSQL 时退化为 JSON 文件存储，重启可恢复。

## 验证结论

- `go build ./...`、`go test ./...` 通过。
- 真机凭据端到端：四类入口全部返回正常结果；refresh-only 账号经保活自动续期后可用；中文 UTF-8 正确。
- 详细证据见 `ACCEPTANCE_bps适配.md`。

## 关键决策记录

- `model` 上游忽略 → 目录保留多模型别名，但实际响应固定 `gpt-5.6-sol`。
- `reasoning.effort` 的 `max` 会被上游 422 → 适配器归一到 `xhigh`。
- 注入提示词默认 `off`（上游自带 4.7 万字符 Excel agent 提示词），需要时管理端开启。
- 不做浏览器登录：账号只从凭据导入，token 靠 refresh token 续期。

## 风险与后续

见 `TODO_bps适配.md`。最重要的两条：上游额度百分比未接、Web 管理台账号导入界面仍按旧 prism 语义（读写接口已适配，纯 UI 文案/表单待更新）。
