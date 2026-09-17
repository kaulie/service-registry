# service-registry 容器镜像（多阶段构建，产物是单二进制，前端已 go:embed）
FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/registryd ./cmd/registryd

FROM alpine:3.20
RUN adduser -D -H -u 10001 registry \
 && mkdir -p /data && chown registry /data
USER registry
ENV REGISTRY_DB_PATH=/data/registry.db \
    REGISTRY_BIND=0.0.0.0 \
    SERVICE_PORT=4240 \
    REGISTRY_DEFAULT_NS=default
VOLUME ["/data"]
# 端口由 SERVICE_PORT 决定（容器里想换端口：-e SERVICE_PORT=9000 -p 9000:9000）。
EXPOSE 4240
COPY --from=build /out/registryd /usr/local/bin/registryd
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s \
  CMD wget -qO- "http://127.0.0.1:${SERVICE_PORT}/health" >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/registryd"]
