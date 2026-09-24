# TODO — bps 适配

## 需要你决定/配置的事

1. **凭据已轮换（重要）**：端到端验证时，保活刷新使 refresh token 发生轮换。
   - `taylor-rt` 账号使用的是**轮换后的新 refresh token**（运行时已存进 `data/state/account/*.json`，保管好该目录即可）。
   - 原 `rt.1.AAAF-...` 是否仍可用未验证；如果失效，请以服务里落库的新值或重新登录为准。
2. **`WEB2API_API_KEY` / `BPS_ENCRYPT_KEY` / `BPS_DB_PASSWORD`**：部署前必须换成随机值（`.env`）。
3. **管理端默认密码**：`admin/admin123`，首次登录强制改密。
4. **镜像仓库**：CI 推到 ghcr.io 免配置；要推 Docker Hub 需在工作流里补 secrets。

## 待开发（按优先级）

| 优先级 | 事项 | 说明 |
|---|---|---|
| P0 | Web 管理台账号导入表单 | 后端接口已支持 access/refresh token；SPA 的 accounts 页仍是旧 prism 文案（cookie/密码/2FA），需改成 token 表单 |
| P1 | 上游额度接入 | `FetchUsage` 目前只用 JWT 身份（email/plan），没接 `chatgpt.com/backend-api/wham/usage`，用量页无百分比、自动暂停不生效 |
| P1 | 多轮会话 | 每次请求新生成 `task_id`/`turn_id`；若上游按 task 维护上下文/计费，需要确定会话关联策略（同 task 复用？） |
| P2 | 服务端工具调用策略 | 上游 agent 会发 `request_user_input_basispoints` 等自带工具的 function_call；当前原样透传给客户端，建议按产品需要拦截或改写为普通文本 |
| P2 | 图片输入真机验证 | `input_image` data URL 路径已实现，未实测 |
| P3 | 注释清理 | 若干注释仍提 prism / sandbox / 预热（无功能影响）；`concurrency_wiring_test.go` 用 prism.openai.com 作测试 URL |
| P3 | 文件存储并发 | JSON 文件后端是进程内锁；多副本部署请使用 PostgreSQL |

## 缺失配置清单（当前环境）

- 本机没有 PostgreSQL / Docker：文件存储模式已验证；生产建议 PG。
- `web/dist` 已构建（本地 npm），CI/Docker 会在镜像内构建。
