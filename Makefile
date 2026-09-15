# service-registry —— 全局独立的服务注册中心（元信息存储中心）
#
# 常用：make lint test / make build / make run

BINARY    := bin/registryd
PKG       := ./cmd/registryd
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(VERSION)

# Go 缓存隔离在仓库之外：控制面调用 build.sh 时 HOME 可能指向 runtime 目录，
# 缓存落到那里会被下一次 rsync --delete 波及。
BUILD_CACHE ?= $(TMPDIR)/service-registry-build-cache

.PHONY: all build test race lint fmt fmt-check vet run clean docker panel-check panel-smoke help

all: lint test panel-smoke build

build: ## 构建本机二进制到 bin/
	@mkdir -p bin
	GOMODCACHE=$(BUILD_CACHE)/gomodcache GOCACHE=$(BUILD_CACHE)/gocache \
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)
	@echo "built $(BINARY) ($(VERSION))"

test: ## 全部单元测试
	go test ./...

race: ## 竞态检测（并发写 + long-poll/SSE 是关键路径）
	go test -race -count=1 ./...

lint: fmt-check vet ## 格式 + go vet

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l . | grep -v '^$$' || true); \
	if [ -n "$$out" ]; then echo "gofmt required for:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

run: ## 本机运行（数据落在 ./data/registry.db，监听 127.0.0.1:4240）
	go run $(PKG)

panel-check: ## 检查面板资源是否可被 go:embed 编入（防止漏文件）
	go build ./web/

panel-smoke: ## 面板交互冒烟：在受控 DOM 里模拟“填表→点提交”，断言失败不静默（需 node）
	@if command -v node >/dev/null 2>&1; then node web/panel-smoke.mjs; \
	else echo "skip: 未安装 node，跳过面板冒烟"; fi

clean:
	rm -rf bin outputs data

docker: ## 构建容器镜像
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t service-registry:$(VERSION) .

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-14s %s\n", $$1, $$2}'
