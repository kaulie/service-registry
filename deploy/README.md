# 打包与部署（遵循部署系统规范）

本项目按 **agent-control-plane-deployment**（部署控制面，`http://127.0.0.1:4220`，
面板 `/panel/`）的规范组织打包与部署脚本。

## 规范摘要

| 环节 | 约定 |
|---|---|
| 打包 | 仓库根 `build.sh`，调用方传 `APP_VERSION=<8 位短 hash>`，cwd = 仓库根；**必须产出 `outputs/`**，其中**必须含 `scripts/restart.sh`** |
| 发版包 | `outputs/` 的完整内容 + `VERSION` / `COMMIT` / `GIT_REPO_URL`（后三者由调用方写入） |
| 部署 | 下载制品 → `rsync -a --delete` 到 `runtimeDir` → 执行 `restartCmd` → 探活 `healthUrl` |
| 保留路径 | 只有 `backend/.env`、`backend/data/`、`backend/runtime.pid`、`backend/server.log` 会在部署时保留，其余一律被包内容替换 |
| 注入环境 | `restartCmd` 以 cwd=`runtimeDir` 执行，并注入 `PORT`（取自 healthUrl）、`RUNTIME_DIR`、`APP_VERSION`。本服务的 `scripts/start.sh` 用 `SERVICE_PORT` > `PORT` > `4240` 决定端口，登记脚本 `deploy/platform/register-service.sh` 的 healthUrl 也默认取 `SERVICE_PORT` |
| 健康检查 | 约定 `http://127.0.0.1:<port>/health`（应用同时保留 `/healthz`） |

## 本项目的落地

```
build.sh                              # 打包：outputs/bin/registryd（前端已 go:embed 进二进制）
scripts/start.sh                      # 启动：生成/读取 backend/.env，探活 /health，写 pid
scripts/stop.sh                       # 停止：TERM → KILL
scripts/restart.sh                    # 重启（控制面默认 restartCmd）
deploy/platform/register-service.sh   # 在控制面登记服务契约（幂等）
client/register.sh                    # 把一个服务的契约+实例登记进**本注册中心**（一次性）
```

runtime 布局（`REGISTRY_RUNTIME_DIR`，默认 `/Users/gaolei/runtime/service-registry`）：

```
bin/registryd             ← 发版包（控制面板资源已编入二进制，无需额外文件）
scripts/*.sh              ← 发版包
backend/.env              ← 保留：管理令牌 + 可选覆盖（首次启动自动生成，0600）
backend/data/registry.db  ← 保留：元信息库（服务契约 + 实例 + 变更/审计）
backend/runtime.pid       ← 保留：进程号
backend/server.log        ← 保留：日志
VERSION / DEPLOYMENT / COMMIT   ← 平台写入
```

> **为什么运行时状态必须放 `backend/`**：部署是 `rsync --delete`，只有上面那几个
> 路径被保留。把库或密钥放在 `bin/`、`scripts/` 或 runtime 根目录，下一次部署就会被删掉。

## 首次接入（一次性）

```bash
# 1. 登记服务契约（幂等，不触发部署）
deploy/platform/register-service.sh

# 2. 打包 + 部署（等价于面板上点「打包→部署」）
curl -sS -X POST http://127.0.0.1:4220/api/deploy-notify \
  -H 'content-type: application/json' -d '{"serviceId":"service-registry"}'

# 3. 验证
curl -sS http://127.0.0.1:4240/health
open http://127.0.0.1:4240/panel/     # 控制面板
```

## 独立脚本（不经控制面）

```bash
/Users/gaolei/deployment/bin/release.sh service-registry main     # 打包 → deployment-<hash>
/Users/gaolei/deployment/bin/deploy.sh  service-registry deployment-<hash>
```

## 容器（可选）

```bash
cd deploy && docker compose up --build     # 面板 http://127.0.0.1:4240/panel/
```

镜像里数据落在命名卷 `registry-data`，重启不丢；生产请设置 `REGISTRY_ADMIN_TOKEN`。

## 不要在注册中心里登记它自己

本服务是**元信息存储中心**：它不探活、不心跳、不启停业务进程。把它自己登记进去只会
产生自指数据，没有任何用处（它的存活由上面的部署契约 + watchdog 负责）。
