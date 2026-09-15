#!/usr/bin/env bash
#
# 启动 service-registry —— 遵循部署系统规范的 runtime 脚本。
#
# 由控制面以 restartCmd 调用：cwd = runtimeDir，且注入
#   PORT        = 服务契约 healthUrl 里的端口（本脚本据此绑定监听地址）
#   RUNTIME_DIR = runtimeDir
#   APP_VERSION = 本次部署的 8 位短 hash
#
# runtime 布局（backend/ 下的内容由平台在部署时保留，不会被 --delete 清掉）：
#   bin/registryd           可执行文件（来自发版包）
#   scripts/*.sh            本目录（来自发版包）
#   backend/.env            管理令牌与可选覆盖项（首次启动自动生成，权限 600）
#   backend/data/           SQLite 元信息库（服务契约 + 实例 + 变更/审计）
#   backend/runtime.pid     进程号
#   backend/server.log      标准输出/错误
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="${RUNTIME_DIR:-$(cd "${DIR}/.." && pwd)}"
PORT="${PORT:-4240}"
APP_VERSION="${APP_VERSION:-dev}"

BIN="${RUNTIME_DIR}/bin/registryd"
BACKEND="${RUNTIME_DIR}/backend"
ENV_FILE="${BACKEND}/.env"
DATA_DIR="${BACKEND}/data"
PID_FILE="${BACKEND}/runtime.pid"
LOG_FILE="${BACKEND}/server.log"

log() { echo "[start] $*"; }
die() { echo "[start][错误] $*" >&2; exit 1; }

[ -x "${BIN}" ] || die "缺少可执行文件 ${BIN}（发版包内容不完整？）"

mkdir -p "${DATA_DIR}"

# 首次启动生成 backend/.env：只放管理令牌与可选覆盖项，权限 600，绝不入 git。
# 监听地址/库路径由本脚本按端口与 RUNTIME_DIR 推导，避免契约换端口后
# .env 里的旧值把服务卡在旧端口上。
if [ ! -f "${ENV_FILE}" ]; then
  umask 077
  TOKEN="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  cat > "${ENV_FILE}" <<EOF
# service-registry 运行期配置（首次启动自动生成，权限 600，请勿提交到 git）
# 管理令牌：命名空间创建/令牌轮换/命名空间删除需要它；为空则写接口不鉴权（仅本机开发）。
REGISTRY_ADMIN_TOKEN=${TOKEN}
# 读接口是否也要求令牌（默认 open：便于各平台拉取元信息）
REGISTRY_READ_AUTH=open
# 变更/审计记录保留天数，0 = 永久保留
# REGISTRY_CHANGE_RETENTION_DAYS=0
# 单个服务内联 OpenAPI 原文大小上限（字节）
# REGISTRY_MAX_SPEC_BYTES=262144
EOF
  chmod 600 "${ENV_FILE}"
  log "已生成 ${ENV_FILE}（含新的 REGISTRY_ADMIN_TOKEN）"
fi

# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a

# 平台注入的值优先：端口永远跟随服务契约的 healthUrl。
export REGISTRY_HTTP_ADDR="${REGISTRY_BIND:-127.0.0.1}:${PORT}"
export REGISTRY_DB_PATH="${REGISTRY_DB_PATH:-${DATA_DIR}/registry.db}"

# 已在运行则不重复拉起（平台重启前都会先 stop，这里是防御性检查）。
if [ -f "${PID_FILE}" ]; then
  old="$(tr -d '[:space:]' < "${PID_FILE}" || true)"
  if [ -n "${old}" ] && kill -0 "${old}" 2>/dev/null; then
    log "已在运行 pid=${old}"
    exit 0
  fi
  rm -f "${PID_FILE}"
fi

log "启动 部署版本=${APP_VERSION} 监听=${REGISTRY_HTTP_ADDR} 库=${REGISTRY_DB_PATH}"
nohup "${BIN}" >> "${LOG_FILE}" 2>&1 &
echo $! > "${PID_FILE}"
pid="$(cat "${PID_FILE}")"

# 探活：/health 是平台对每个服务统一探的路径。
for _ in $(seq 1 40); do
  if ! kill -0 "${pid}" 2>/dev/null; then
    rm -f "${PID_FILE}"
    echo "[start][错误] 进程已退出，最近日志：" >&2
    tail -20 "${LOG_FILE}" >&2 || true
    exit 1
  fi
  if curl -fsS -m 2 "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
    log "启动成功 pid=${pid} log=${LOG_FILE}"
    log "控制面板：http://127.0.0.1:${PORT}/panel/"
    exit 0
  fi
  sleep 0.5
done

echo "[start][错误] 20s 内 /health 未就绪，最近日志：" >&2
tail -20 "${LOG_FILE}" >&2 || true
kill "${pid}" 2>/dev/null || true
rm -f "${PID_FILE}"
exit 1
