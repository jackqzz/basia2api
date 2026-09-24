# ALIGNMENT — bps 适配（把 prism2api 改造为 bps 反代）

## 原始需求

参考 https://github.com/lvzhentao/chatgpt-prism2api（把 prism.openai.com 反代成 OpenAI/Anthropic API 的 Go 项目），
在它基础上改造出 `bps2api`：上游换成 `https://bps.openai.com/basispoints/api/responses`（ChatGPT Excel 插件 agent 端点），
对外继续提供 OpenAI / Anthropic 兼容入口。

## 已确认决策（用户拍板）

| 决策点 | 结论 |
|---|---|
| 底座 | 基于 prism2api 派生，新增 `internal/adapter/bps` 适配器，保留内核（账号池/调度/管理后台/入口） |
| 账号来源 | 导入 `access_token` + `refresh_token`（+ 可推导的 `chatgpt_account_id`），不做浏览器登录 sidecar |
| 入口范围 | `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/models` 全要 |
| 项目位置 | `D:\niubiai.cc\bps2api` |

## 上游协议（已实测，2026-09-24）

- `POST /basispoints/api/responses`，SSE（`text/event-stream`）
- 认证头：`authorization: Bearer <access_token>`、`chatgpt-account-id`、`x-openai-account-id`、`x-basispoints-auth-mode: chatgpt`
- **body 必须带 `metadata.task_id` 与 `metadata.turn_id`**，缺失即 `422`
- `model` 字段被上游忽略，实际固定 `gpt-5.6-sol`
- `instructions` 被上游覆盖为 Excel 插件 4.7 万字符 agent 提示词（服务端注入，客户端删不掉）
- `reasoning.effort` 支持 `low/medium/high/xhigh`；`max` 直接 422；`tool_choice` 422
- 坏 token → 401；`x-basispoints-auth-mode: apikey` 会校验 OpenAI API key
- token 刷新：`POST https://auth.openai.com/oauth/token`（form 表单，`grant_type=refresh_token`，`client_id=app_EMoamEEZ73f0CkXaXp7hrann`，`scope=openid profile email`）

## 需求理解

1. `internal/adapter/bps` 实现 `adapter.Site`（Name/NewClient/NewCatalog/Auth/MapChat）+ `Client`（Stream/ListModels/FetchUsage/SetBaseURL/SetEgress/SetHTTPClient）。
2. 内核中编译期耦合 prism 的 4 个文件（账号导入/机器导入/管理动作/账号池展示）改为 bps 语义。
3. 删除 prism 专有的浏览器登录 sidecar（`cmd/login`、`sidecar/`、`RunSidecarLogin`），凭据只走导入。
4. 品牌与配置更名：`prism` → `bps`（日志、环境变量、默认上游地址、加密目录）。
5. 对外 API 语义不变（OpenAI chat/responses、Anthropic messages、models），非流式由内核聚合，适配器只实现流式。

## 边界（明确不做）

- 不做账号密码 + TOTP 自动登录（sidecar 整套删除）。
- 不改内核对工具调用、用量、重试、选号的机制（只做适配层与必要品牌替换）。
- 不新增/重构内核 REST 路由（沿用上游项目既有路由）。
- `bps` 上游多模态暂按文本 + 图片 data URL 透传（input_image），文件按文本内联；file_id 不做。

## 验收标准

1. `go build ./...`、`go test ./...` 通过。
2. 导入一条真实 ChatGPT 凭据后：
   - `POST /v1/chat/completions`（stream true/false）拿到模型回复；
   - `POST /v1/responses` 拿到 Responses 形状输出；
   - `POST /v1/messages` 拿到 Anthropic 形状输出；
   - `GET /v1/models` 返回静态目录。
3. access token 过期时能用 refresh token 自动续期（保活/请求路径触发）。
4. 无 `metadata.task_id/turn_id` 之外的脏请求体：适配器自动生成 task/turn。
