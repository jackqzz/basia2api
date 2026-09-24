# 部署说明 — excel.niubiai.cc

部署时间：2026-09-24。目标服务器：`185.246.84.164`（Ubuntu 24.04，Docker 29 + Compose 2.40）。

## 拓扑

```
浏览器/客户端 → https://excel.niubiai.cc（Cloudflare 代理）
              → nginx（185.246.84.164:443，Let's Encrypt 证书）
              → 127.0.0.1:8302（Docker 容器 bps-2api:8080）
              → https://bps.openai.com/basispoints/api
```

- 服务器上原有 `niubiai.cc`（sub2api，8080）与 `v.niubiai.cc`（loomic）不受影响。
- PostgreSQL 容器 `bps-2api-pg`，数据卷 `./data-pg`；如不用 PG 可注释 compose 里的 `DATABASE_URL` 退化为 `./data/state` 文件存储。

## 服务器路径与文件

| 路径 | 说明 |
|---|---|
| `/opt/bps2api` | 项目源码 + `.env`（600，含 DB 口令 / API Key / 加密键） |
| `/etc/nginx/sites-available/excel.niubiai.cc` | 反代站点（80 + 443 SSL，`proxy_buffering off`，读写 600s） |
| `/etc/letsencrypt/live/excel.niubiai.cc/` | 证书（2026-12-23 到期，certbot 自动续期） |
| `/opt/bps2api/data-pg` | PostgreSQL 数据目录 |

## 常用操作

```bash
cd /opt/bps2api

# 查看状态 / 日志
docker compose ps
docker compose logs -f bps-2api

# 更新代码后重建（在本地重新打包源码上传后执行）
docker compose up -d --build

# 重启 / 停止
docker compose restart bps-2api
docker compose down

# 重新签发证书（域名解析变化时）
certbot --nginx -d excel.niubiai.cc --non-interactive --agree-tos --redirect
```

## 本地打包上传（Windows）

```powershell
tar.exe -czf bps2api-src.tar.gz --exclude=.git --exclude='web/node_modules' --exclude='web/dist' -C <项目目录> .
scp -i C:\Users\Administrator\.ssh\id_ed25519 bps2api-src.tar.gz root@185.246.84.164:/tmp/
ssh -i C:\Users\Administrator\.ssh\id_ed25519 root@185.246.84.164 "rm -rf /opt/bps2api/* && tar -xzf /tmp/bps2api-src.tar.gz -C /opt/bps2api && cd /opt/bps2api && docker compose up -d --build"
```

注意：`.env` 不会被覆盖（打包时未包含；重建前确认 `/opt/bps2api/.env` 仍在）。

## 账号导入（示例）

```bash
curl -X POST http://127.0.0.1:8302/api/v1/accounts/import \
  -H "Authorization: Bearer <WEB2API_API_KEY>" -H "Content-Type: application/json" \
  -d '{"accounts":[{"name":"taylor","email":"...","access_token":"eyJ...","refresh_token":"rt.1...","chatgpt_account_id":"0ab39862-..."}],"overwrite":true}'
```

## 注意事项

- 管理后台 `https://excel.niubiai.cc/admin/` 默认 `admin/admin123`，**首次登录强制改密**后才可调用管理接口。
- `.env` 里的 `WEB2API_API_KEY` / `BPS_ENCRYPT_KEY` / `BPS_DB_PASSWORD` 是部署时随机生成的，不要提交到 Git，也不要泄露。
- Cloudflare 侧建议 SSL 模式 Full (strict)（源站已是有效 LE 证书）。
