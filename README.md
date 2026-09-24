# bps-2api

把 **bps.openai.com/basispoints**（ChatGPT Excel 插件的 agent 端点）反代成 OpenAI / Anthropic 兼容 API。

在 [chatgpt-prism2api](https://github.com/lvzhentao/chatgpt-prism2api) 的架构上派生：账号池 / 调度 / 保活 / 管理后台 / 多协议入口全部复用，上游适配器换成 `internal/adapter/bps`。

## 能力

| 入口 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions（stream true/false） |
| `POST /v1/responses` | OpenAI Responses（Codex 客户端可直接接） |
| `POST /v1/messages` | Anthropic Messages（Claude Code 可直接接） |
| `GET /v1/models` | 静态模型目录（gpt-5.6-sol / gpt-6-astra / gpt-5.6-terra / gpt-5.6-luna / gpt-5.5） |

- 账号池轮询 / 优先级 / 粘性会话 / 失败换号（内核调度器）
- **refresh_token 自动续期**：access token 过期前 15 分钟保活预刷新；只有 refresh_token 的账号首轮保活后自动可用
- 存储：PostgreSQL（推荐）或 **无库时的本地 JSON 文件存储**（`WEB2API_CREDENTIAL_DIR` 下）
- 管理后台 `/admin/`（默认 admin / admin123，首次登录强制改密）
- 多格式账号导入：ChatGPT auth JSON / CSV / `----` 分隔行 / 机器导入 API

## 快速开始（单机，无 PostgreSQL）

```bash
go build -o bps-server ./cmd/server
WEB2API_CREDENTIAL_DIR=./data WEB2API_API_KEY=sk-your-key ./bps-server --listen 127.0.0.1:8080
```

导入账号（`POST /api/v1/accounts/import`，Bearer = `WEB2API_API_KEY`）：

```bash
curl -X POST http://127.0.0.1:8080/api/v1/accounts/import \
  -H "Authorization: Bearer sk-your-key" -H "Content-Type: application/json" \
  -d '{"accounts":[{
        "name":"taylor",
        "access_token":"eyJhbGciOi...",
        "refresh_token":"rt.1....",
        "chatgpt_account_id":"0ab3...."
      }]}'
```

调用：

```bash
curl -X POST http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"你好"}],"stream":false}'
```

## 账号导入格式

`POST /api/admin/accounts/import` 的 `text` 字段支持：

1. **ChatGPT auth JSON**（用户登录态直出，推荐）

```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "rt.1....",
  "chatgpt_account_id": "0ab39862-...",
  "email": "user@example.com",
  "proxy_url": "http://user:pass@host:port"
}
```

2. **CSV**：`email,access_token,refresh_token,chatgpt_account_id,proxy`（表头可换序，认不出表头按位置）
3. **`----` 分隔行**：`email----access_token----refresh_token`
4. 纯 access token（JWT）一行一个；`chatgpt_account_id` 自动从 JWT claim 推导

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `VENDOR_API_BASE_URL` | `https://bps.openai.com/basispoints/api` | 上游基址 |
| `WEB2API_API_KEY` / `-auth-key` | 空（不鉴权） | 本站 Bearer 鉴权（入口 + 机器导入） |
| `WEB2API_DATABASE_URL` | 空 → JSON 文件存储 | PostgreSQL 连接串 |
| `WEB2API_CREDENTIAL_DIR` | `~/.bps-2api` | 文件存储 / 凭据目录 |
| `WEB2API_DEFAULT_MODEL` | `gpt-5.6-sol` | 兜底模型 |
| `WEB2API_MAX_EFFORT` | `high` | Anthropic 入口思考档位上限 |
| `WEB2API_KEEPALIVE` | 开 | `off` 关闭保活刷新 |
| `BPS_SYSTEM_PROMPT(_FILE)` | 空（off） | 附加系统指令（上游自带 Excel agent 提示词，默认不叠加） |
| `WEB2API_ENCRYPT_KEY` | 空（不加密） | 落库凭据 AES-256-GCM 密钥 |

管理后台运行时配置（`/api/admin/config`）可热改：路由策略、并发、重试、代理、注入提示词等。

## 上游特性与限制（实测 2026-09-24）

- 请求体必须带 `metadata.task_id` / `metadata.turn_id`（缺了 422），适配器自动生成
- `model` 字段被上游忽略，实际固定 `gpt-5.6-sol`
- `instructions` 被上游覆盖为 Excel 插件 4.7 万字符 agent 提示词（服务端注入，删不掉）
- `reasoning.effort` 支持 `low/medium/high/xhigh`；**`max` 会 422**（适配器归一到 `xhigh`）
- `tool_choice` 不被接受；`tools`（function 定义）可透传
- 认证：`authorization: Bearer <access_token>` + `chatgpt-account-id` + `x-basispoints-auth-mode: chatgpt`

## 部署

```bash
cp .env.example .env    # 改 BPS_DB_PASSWORD / WEB2API_API_KEY / BPS_ENCRYPT_KEY
docker compose up -d --build
```

不用 PostgreSQL：注释掉 compose 里的 `DATABASE_URL`，数据落在 `./data/state/`。

无账号自测：`docker compose --profile mock up -d`（内置 mock 上游，端口 18080）。

## 测试

```bash
go test ./...                     # 单测
python3 scripts/smoke.py --only L0,L1   # 端到端冒烟（需真实账号与 WEB2API_URL/WEB2API_API_KEY）
```

## 与 prism2api 的差异

- 删除 prism 适配器与浏览器登录 sidecar（`cmd/login`、`sidecar/`、`Dockerfile.login`）
- 新增 `internal/adapter/bps`（凭据解析 / OAuth 刷新 / Responses SSE 映射）
- 无 PostgreSQL 时可退化本地 JSON 文件存储
- 品牌与默认值：`bps 2API`、上游 `bps.openai.com`、默认模型 `gpt-5.6-sol`
