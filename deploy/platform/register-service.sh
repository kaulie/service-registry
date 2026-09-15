#!/usr/bin/env bash
#
# 在部署系统（agent-control-plane-deployment, :4220）里登记 service-registry 的服务契约。
#
# 契约即平台运行一个服务的全部约定：runtime 目录、健康检查 URL、启停命令。
# 幂等：重复执行即更新；不改动其它服务，也不触发部署。
#
# 用法：
#   deploy/platform/register-service.sh
#   REGISTRY_SERVICE_PORT=4240 REGISTRY_RUNTIME_DIR=/Users/gaolei/runtime/service-registry \
#     deploy/platform/register-service.sh
#
# 登记后即可在面板 http://localhost:4220/panel/ 里打包 / 部署，或用 API：
#   curl -sS -X POST http://127.0.0.1:4220/api/deploy-notify \
#        -H 'content-type: application/json' -d '{"serviceId":"service-registry"}'
#
# 注意：变量统一用 REGISTRY_ 前缀。HOST / PORT 这类通用名字在 CI/沙箱里常已被占用，
# 直接读取会把服务登记到错误的端口上。
set -euo pipefail

REGISTRY_CONTROL_PLANE="${REGISTRY_CONTROL_PLANE:-http://127.0.0.1:4220}"
REGISTRY_SERVICE_ID="${REGISTRY_SERVICE_ID:-service-registry}"
REGISTRY_RUNTIME_DIR="${REGISTRY_RUNTIME_DIR:-/Users/gaolei/runtime/${REGISTRY_SERVICE_ID}}"
REGISTRY_SERVICE_PORT="${REGISTRY_SERVICE_PORT:-4240}"
REGISTRY_GIT_REPO_URL="${REGISTRY_GIT_REPO_URL:-https://github.com/kaulie/service-registry}"
REGISTRY_DEFAULT_BRANCH="${REGISTRY_DEFAULT_BRANCH:-main}"
REGISTRY_SERVICE_NAME="${REGISTRY_SERVICE_NAME:-Service Registry (全局服务注册中心)}"

case "${REGISTRY_SERVICE_PORT}" in
  *[!0-9]*|"") echo "REGISTRY_SERVICE_PORT 必须是数字，当前：${REGISTRY_SERVICE_PORT}" >&2; exit 1 ;;
esac

echo "==> 登记服务契约 serviceId=${REGISTRY_SERVICE_ID} port=${REGISTRY_SERVICE_PORT} runtime=${REGISTRY_RUNTIME_DIR}"

curl -sS -X PUT "${REGISTRY_CONTROL_PLANE}/api/services/${REGISTRY_SERVICE_ID}" \
  -H 'content-type: application/json' \
  -d @- <<JSON
{
  "name": "${REGISTRY_SERVICE_NAME}",
  "runtimeDir": "${REGISTRY_RUNTIME_DIR}",
  "healthUrl": "http://127.0.0.1:${REGISTRY_SERVICE_PORT}/health",
  "startCmd": "bash \"${REGISTRY_RUNTIME_DIR}/scripts/start.sh\"",
  "stopCmd": "bash \"${REGISTRY_RUNTIME_DIR}/scripts/stop.sh\"",
  "restartCmd": "bash \"${REGISTRY_RUNTIME_DIR}/scripts/restart.sh\"",
  "gitRepoUrl": "${REGISTRY_GIT_REPO_URL}",
  "defaultBranch": "${REGISTRY_DEFAULT_BRANCH}"
}
JSON
echo
echo "==> 完成。面板：${REGISTRY_CONTROL_PLANE}/panel/"
echo "==> 注意：本服务是**元信息存储中心**，不探活、不心跳、不启停任何业务进程；"
echo "    它自己由上面的契约启停，别把它注册进它自己（避免自指）。"
