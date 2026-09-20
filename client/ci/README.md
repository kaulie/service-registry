# CI 自动登记契约（Go + swaggo/swag）

目标：**接口改了就自动刷新注册中心里的契约**，没有人手工维护 OpenAPI 文件。

```
代码注解（swag）──swag init──▶ docs/swagger.json ──register.sh──▶ 注册中心（PUT 契约 + PUT 实例）
   唯一真源                         CI 产物                      幂等：没变化就不写库
```

注册中心**不会出网抓取**（设计红线：无 SSRF 面、无定时任务），所以"取规范 + 上报"这一步必须发生在
**你们这一侧**（CI / build.sh / 部署后钩子）。

## 1. 一次性改造（每个 Go 服务）

```go
// cmd/<service>/main.go —— General API Info（swag 的"注解入口"）
// @title           event-center
// @version         1.4.2
// @description     事件中心：注入与拉取
// @BasePath        /
// @host            127.0.0.1:9099
```
```go
// internal/api/handlers.go —— 每个 handler 一段注解，这就是"注解式声明 API"
// @Summary  拉取事件
// @Tags     events
// @Produce  json
// @Param    stream  path  string  true  "流名"
// @Success  200  {object}  Stream
// @Router   /v1/streams/{stream}/events [get]
func handleStreamEvents(w http.ResponseWriter, r *http.Request) { ... }
```
> 注解只影响生成规范，**运行时零依赖**（handler 不需要 import swag 的任何包）。想让服务自己也暴露
> `/openapi.json`，再 `go:embed` 一份生成结果即可（可选）。

## 2. 本地验证

```bash
go install github.com/swaggo/swag/cmd/swag@latest   # 首次
swag init -g cmd/event-center/main.go -o docs       # 产物 docs/swagger.json
# 直接登记一次（幂等：重复跑不会写库）
SERVICE_NAME=event-center INSTANCES=127.0.0.1:9099 \
DEPARTMENT_ID=D0005 OWNER=kaulie VERSION=1.4.2 \
  bash client/ci/register-go-service.sh
```

## 3. 挂到 CI

一条命令即可 —— **读代码（swag init）→ 上报（register.sh）** 都在里面：

```bash
SERVICE_NAME=$SERVICE_NAME \
REGISTRY_URL=http://127.0.0.1:4240 \
REGISTRY_NS=$REGISTRY_NS REGISTRY_TOKEN=$NS_TOKEN \
DEPARTMENT_ID=D0005 \
INSTANCES=127.0.0.1:9099 \
GIT_REPO=$GIT_REPO \
OWNER=$OWNER HEALTH_PATH=/health TAGS=events \
  bash client/ci/register-go-service.sh
```

触发点选哪个（**都能满足"CI 阶段读代码、主动登记"**）：

| 触发点 | 说明 |
|---|---|
| 本机 / self-hosted runner 上的 CI | 推荐。注册中心默认**只绑 127.0.0.1**（写接口默认开放，所以刻意不暴露到网络），只有本机的 runner 能直连 |
| `build.sh` / 发布脚本末尾 | 最省事：发版流程本来就跑在本机，加一行即可 |
| 部署平台部署成功后 | 部署平台手里有 `serviceId`/`port`/`runtimeDir`/`gitRepoUrl`，理想位置（另见 `docs/` 里的约定） |
| GitHub-hosted runner | ⚠️ **默认够不到 `127.0.0.1:4240`**。要么换 self-hosted，要么等注册中心"远端暴露"（见 `docs/DESIGN.md` 路线图）后再用 |

模板：`client/ci/github-actions.example.yml`（复制到服务仓库的 `.github/workflows/`，把 runner 改成 self-hosted）。

## 4. 令牌与命名空间

- 写接口现在**默认开放**（`REGISTRY_WRITE_AUTH=open`，启动有 WARN、`/v1/meta.writeAuth` 可见）——
  内网把链路跑通优先；所以现阶段 CI 可以不带令牌。
- 收紧后：`POST /v1/namespaces {name}` 创建命名空间会**一次性**返回 `rt_...` 令牌（之后只能轮换、不能再看），
  在 CI 里以 `REGISTRY_TOKEN` 传（走密钥管理，别写进仓库）；该令牌**只能写自己那个命名空间**。

## 5. 幂等与"别刷 revision"

`register.sh` 登记前会比对**规范原文的 sha256** 与服务端存的 `api.specHash`：一致就跳过 PUT
（不产生无意义的变更记录，消费方的增量游标不会被噪声刷屏），实例集合仍按声明式对齐。
要强制覆盖加 `--force`。同一份规范重复跑：第 2 次起都是"跳过"。

## 6. 常见坑

| 现象 | 原因 / 处理 |
|---|---|
| `paths` 是空的 | 注解没被识别：General API Info 必须写在 `swag init -g` 的那个文件顶部；每个接口要有 `@Router` |
| 接口在子包/依赖里没被扫到 | 加 `SWAG_ARGS="--parseDependency --parseInternal"`（按需，扫描范围越大越慢） |
| 生成的版本号还是旧的 | `@version` 是手写的：CI 里用 `VERSION=$(git describe --tags --always)` 覆盖，或在注解里用变量（`swag init --instanceName` 等） |
| 规范超过 256KB | 注册中心单条内联 spec 上限 `REGISTRY_MAX_SPEC_BYTES`（默认 256KB）→ 拆服务或去掉 `--parseDependency` 的冗余 |
| 服务是 Node/Python | 同一套流程，只是第一步换成 `@fastify/swagger`（路由 schema 即注解）或 FastAPI 自带的 `/openapi.json`：`register.sh --spec-url http://127.0.0.1:<port>/openapi.json` |
| 想把 `docs/` 提交进仓库 | 可以，但**别手工改** `docs/swagger.json`（它是产物）；要么 CI 里生成、要么生成后提交并在 CI 里 `--force` 校验一致 |

## 7. 相关文件

- `client/register.sh`：真正的登记器（`--file` / `--spec-url` / `--force`、契约 + 实例、幂等）
- `client/ci/register-go-service.sh`：Go 服务一条命令（swag init + register.sh）
- `client/ci/github-actions.example.yml`：workflow 模板
- `README.md`「用脚本一次性登记（推荐给 CI）」：手工/CI 的最短路径
