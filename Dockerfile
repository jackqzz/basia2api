# ---- 前端（锁文件未变则命中 npm 层）----
FROM node:20-alpine AS web

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Go 构建（go.mod 未变则命中依赖层）----
FROM golang:1.23-alpine AS builder

ENV GOPROXY=https://proxy.golang.org,https://goproxy.cn,direct

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bps2api ./cmd/server

# ---- 运行 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata su-exec wget && \
    adduser -D -u 10001 app

WORKDIR /app
COPY --from=builder /out/bps2api /app/bps2api
COPY --from=builder /src/entrypoint.sh /app/entrypoint.sh
COPY --from=web /web/dist /app/web
RUN chmod +x /app/entrypoint.sh && \
    mkdir -p /data && chown app:app /data

VOLUME /data

ENV WEB2API_ADMIN_STATIC=/app/web \
    WEB2API_ALLOW_REMOTE_ADMIN=true \
    TZ=Asia/Shanghai

EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["--listen", "0.0.0.0:8080"]
