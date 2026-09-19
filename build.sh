#!/usr/bin/env bash
#
# 打包脚本 —— 遵循「agent-control-plane-deployment」部署系统规范。
#
# 调用方（二选一，均从仓库根执行）：
#   - 控制面流水线：POST /api/deploy-notify {serviceId:"service-registry"}
#   - 独立发版：/Users/gaolei/deployment/bin/release.sh service-registry [ref]
#
# 约定：
#   - cwd = 仓库根；环境变量 APP_VERSION = <8 位短 hash>
#   - 必须产出 outputs/，其中必须包含 scripts/restart.sh（平台硬性要求）
#   - VERSION / COMMIT / GIT_REPO_URL 由调用方写入发版包，本脚本不写
#   - 运行期可变内容一律不放进 outputs/（数据库、密钥、pid、日志都在 backend/ 下，
#     由平台在部署时保留，见 scripts/start.sh）
#
# 注意：控制面板是**编进二进制**的（web/ 用 go:embed），所以发版包里不需要
# 再单独拷贝前端资源。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT}"

VERSION="${APP_VERSION:-dev}"
OUT="${ROOT}/outputs"

echo "[build] service-registry version=${VERSION}"
rm -rf "${OUT}"
mkdir -p "${OUT}/bin" "${OUT}/scripts"

# Go 缓存/临时目录隔离在仓库之外。默认每次打包都使用全新的临时目录，
# 避免复用被上一次失败构建污染（例如模块缓存里只留下了目录、缺了文件）的
# 旧缓存，否则会报 "no required module provides package ..."。
# REGISTRY_BUILD_CACHE 仍可显式指定（用于本地复现/预热缓存）。
if [ -n "${REGISTRY_BUILD_CACHE:-}" ]; then
  BUILD_CACHE="${REGISTRY_BUILD_CACHE}"
else
  BUILD_CACHE="$(mktemp -d "${TMPDIR:-/tmp}/service-registry-build-cache.XXXXXX")"
  cleanup_build_cache() {
    local status=$?
    chmod -R u+w "${BUILD_CACHE}" 2>/dev/null || true
    rm -rf "${BUILD_CACHE}" 2>/dev/null || true
    exit "${status}"
  }
  trap cleanup_build_cache EXIT
fi
export GOMODCACHE="${BUILD_CACHE}/gomodcache"
export GOCACHE="${BUILD_CACHE}/gocache"
export GOPATH="${BUILD_CACHE}/gopath"
export GOTMPDIR="${BUILD_CACHE}/gotmp"
mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOPATH}" "${GOTMPDIR}"
# 默认用可达的模块/工具链代理（部分网络下 proxy.golang.org 走 IPv6 不可达）。
[ -n "${GOPROXY:-}" ] || export GOPROXY="https://goproxy.cn,direct"
# 使用控制面已安装的 Go 工具链构建，避免自动下载不完整的 go1.25 工具链。
[ -n "${GOTOOLCHAIN:-}" ] || export GOTOOLCHAIN="local"

LDFLAGS="-s -w -X main.version=${VERSION}"

# 本机二进制：平台 runtime 直接运行它。
CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" \
  -o "${OUT}/bin/registryd" ./cmd/registryd

# 交叉编译 linux/amd64 只在部署到远端（cloud-server / 容器）时需要：
#   REGISTRY_BUILD_LINUX=1 ./build.sh
if [ "${REGISTRY_BUILD_LINUX:-0}" = "1" ]; then
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" \
    -o "${OUT}/bin/registryd-linux-amd64" ./cmd/registryd
fi

# 平台通过 service contract 的 startCmd/stopCmd/restartCmd 调用这三个脚本。
cp "${ROOT}/scripts/start.sh" "${ROOT}/scripts/stop.sh" "${ROOT}/scripts/restart.sh" \
  "${OUT}/scripts/"

chmod +x "${OUT}/bin/"* "${OUT}/scripts/"*.sh

echo "[build] outputs 就绪："
ls -1 "${OUT}" "${OUT}/bin" "${OUT}/scripts" | sed 's/^/  /'
