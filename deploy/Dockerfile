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
    REGISTRY_HTTP_ADDR=0.0.0.0:4240 \
    REGISTRY_DEFAULT_NS=default
VOLUME ["/data"]
EXPOSE 4240
COPY --from=build /out/registryd /usr/local/bin/registryd
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:4240/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/registryd"]
