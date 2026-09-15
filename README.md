# service-registry

**全局独立的服务注册中心（元信息存储中心）。**

服务契约（含**对外 API 这一基础属性**）与实例元信息在这里登记；所有其他平台
**从这里拉取**服务元信息。下游是拉模型：本服务不主动找任何人。

```
 各服务 / CI / 部署流水线 ──PUT(一次性登记)──▶  service-registry  :4240  ◀──pull──  部署控制面 :4220
                                              （唯一真源：               ◀──pull──  agent-watchdog :4230
                                                契约 + API + 实例        ◀──pull──  网关 / 运维脚本 / 其他平台
                                                + 变更游标 + 审计）
```

## 语义边界（务必先读）

| ✅ 本中心做什么 | ❌ 本中心不做什么 |
|---|---|
| 存储服务契约：身份、版本、owner、标签、**对外 API**（内联 OpenAPI 或显式端点）、文档链接 | **不探活**：不主动请求任何实例 |
| 存储实例元信息：地址、端口、自定义 metadata、来源与时间 | **不心跳**：服务运行时与本中心零连接，不需要任何常驻客户端 |
| 变更可追溯：全局递增 `revision` + 变更日志 + 审计（谁、何时、改了什么） | **不启停**任何进程（自愈是 watchdog 的职责，它可以拉我们的数据） |
| 提供查询 / 反查 / 快照 / 增量游标 / SSE 实时订阅给所有平台 | **不保证可用性**：不返回"健康/up"状态，可达性由消费方自行校验 |
| 独立控制面板（原生 HTML/CSS/JS，同端口托管） | 不做多节点集群/选主/federation（单实例、唯一真源） |

契约里的 `healthPath` / `docsUrl` / `specUrl` **只是元信息字段**：本中心存着它们，
供消费方或 watchdog 自行使用，本中心自己不会去访问。

## 快速开始

```bash
# 1. 本机跑起来（零依赖：单二进制 + SQLite，监听 127.0.0.1:4240）
go run ./cmd/registryd
#   控制面板：http://127.0.0.1:4240/panel/
#   数据：./data/registry.db
#   ⚠️ 写接口**默认开放**（REGISTRY_WRITE_AUTH=open）：不带令牌即可登记/维护。
#      收紧：REGISTRY_WRITE_AUTH=token 重启即可（客户端带 admin / 命名空间令牌）。

# 2. 或用容器
cd deploy && docker compose up --build

# 3. 或用部署控制面（打包 → 部署 → 探活）
deploy/platform/register-service.sh
curl -sS -X POST http://127.0.0.1:4220/api/deploy-notify \
  -H 'content-type: application/json' -d '{"serviceId":"service-registry"}'
```

## 一次完整的接入：登记 → 发现 → 拉取

示例用 admin 令牌（以 `REGISTRY_ADMIN_TOKEN=adm` 启动）；开发模式可省略令牌头。

```bash
B=http://127.0.0.1:4240
A='Authorization: Bearer adm'
J='content-type: application/json'

# 0) 建命名空间（返回**一次性**注册令牌，之后只能轮换、不能再次查看）
curl -sS -X POST $B/v1/namespaces $J -H "$A" \
  -d '{"name":"team-a","description":"A 组的服务"}'
# → {"namespace":{...},"token":"rt_..."}

# 1) 登记服务契约 —— 服务的对外 API 是基础属性，必须给出
curl -sS -X PUT $B/v1/namespaces/team-a/services/event-center $J -H "$A" -d @- <<'JSON'
{
  "version": "1.4.2", "owner": "kaulie", "description": "统一事件中心",
  "tags": ["events", "pubsub"], "healthPath": "/health",
  "api": {
    "protocols": ["http"],
    "authSchemes": [{"scheme":"bearer","in":"header","name":"Authorization"}],
    "docsUrl": "https://github.com/kaulie/event-center",
    "spec": "openapi: 3.0.3\ninfo:\n  title: event-center\n  version: 0.1.0\npaths:\n  /v1/streams/{stream}/events:\n    get:\n      summary: 按游标拉取事件\n"
  }
}
JSON
# → 201 {"service":{... "api":{"specHash":"...","endpoints":[...]}}}

# 2) 声明实例集合（声明式整组对齐，CI/部署流水线一次性调用即可）
curl -sS -X PUT $B/v1/namespaces/team-a/services/event-center/instances $J -H "$A" -d '{
  "instances":[
    {"scheme":"http","host":"10.0.0.7","port":9099,"metadata":{"zone":"edge"}},
    {"scheme":"http","host":"10.0.0.8","port":9099,"metadata":{"zone":"edge"}}
  ]}'
# → {"created":2,"updated":0,"deleted":0,"unchanged":0,"instances":[...]}

# 3) 发现：目录 / 详情 / 实例 / 随机取一个 / 按元信息过滤
curl -sS "$B/v1/services?tag=events"
curl -sS $B/v1/namespaces/team-a/services/event-center
curl -sS "$B/v1/namespaces/team-a/services/event-center/instances?pick=random"
curl -sS "$B/v1/namespaces/team-a/services/event-center/instances?meta=zone=edge"

# 4) 反查："这个接口谁提供"（具体路径会自动命中登记模板）
curl -sS "$B/v1/search/apis?method=GET&path=/v1/streams/abc/events"
# → {"matches":[{"namespace":"team-a","service":"event-center","matchType":"template", ...}]}
curl -sS "$B/v1/search/apis?path=/v1/**"      # 通配：* 单段、** 跨段

# 5) 取回内联 OpenAPI 原文（喂给工具链）
curl -sS $B/v1/namespaces/team-a/services/event-center/spec -o openapi.yaml
```

### 用脚本一次性登记（推荐给 CI）

```bash
REGISTRY_TOKEN=rt_xxx client/register.sh \
  --service event-center --file api/openapi.yaml \
  --instance 10.0.0.7:9099 --instance 10.0.0.8:9099 \
  --owner kaulie --version 1.4.2 --tag events

# 只更新契约 / 只更新实例
client/register.sh --service event-center --file api/openapi.yaml
client/register.sh --service event-center --instance 10.0.0.7:9099 --no-contract
```

> `client/register.sh` 是**一次性调用**：跑完就退出。它不是守护进程，不维持心跳，
> 被登记的服务也不需要做任何改造。

## 给其他平台拉取（拉模型三件套）

| 场景 | 接口 | 说明 |
|---|---|---|
| 冷启动全量 | `GET /v1/snapshot` | 带 `ETag`；下次带上 `If-None-Match`，没变化就是 `304`（零响应体） |
| 增量同步 | `GET /v1/changes?since=<last>` | 每个变更都有**全局递增 revision**；保存最后一次即可 |
| 挂起等变更 | `GET /v1/changes?since=<last>&wait=30s` | long-poll：有新变更立刻返回，否则最多挂 30s（`waited=true`） |
| 实时推送 | `GET /v1/events` | SSE：`hello`（当前游标）+ `change`（每条变更）+ `: ping` 保活 |
| 审计回溯 | `GET /v1/audit?entity=service&op=update` | 同一张变更表，含 actor 与人类可读说明 |

三者共享同一份变更日志，因此**快照 / 增量 / 实时永远一致**。

```bash
# 增量同步的典型循环
curl -sS "$B/v1/snapshot" -D- -o snap.json | grep -i etag     # 首次拿全量 + ETag
last=$(python3 -c 'import json;print(json.load(open("snap.json"))["revision"])')
while :; do
  out=$(curl -sS "$B/v1/changes?since=$last&wait=30s&limit=200")
  echo "$out" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for c in d["changes"]:
    print(c["revision"], c["op"], c["entity"], c["ref"], "-", c.get("detail",""))
print("NEXT", d["revision"])'
  last=$(echo "$out" | python3 -c 'import json,sys;print(json.load(sys.stdin)["revision"])')
done
```

## 控制面板

`http://127.0.0.1:4240/` → 302 `/panel/`（原生 HTML + CSS + JS，无框架无构建步骤，
后端同端口托管，与 `/v1/*` 完全隔离）：

| 页签 | 能力 |
|---|---|
| 概览 | 实时计数、当前 revision、语义边界、各拉取接口的可复制 curl |
| 服务目录 | 契约属性 + **API 端点表**（方法/路径/说明/标签/鉴权）+ 查看/下载内联 spec + 用真实实例生成调用示例 |
| API 检索 | 按方法/路径（具体路径、模板、通配）反查提供方 |
| 实例 | 地址/metadata/来源，注销单个实例 |
| 变更与审计 | 按 revision 时间线回溯"谁改了什么" |
| 命名空间 | 列表、创建、轮换/清除注册令牌 |
| 实时事件流 | 直接消费 `/v1/events`（SSE），观察上下线变更 |

## 鉴权与命名空间

| 令牌 | 能做什么 |
|---|---|
| `REGISTRY_ADMIN_TOKEN`（admin） | 全权：任意命名空间读写、创建/删除命名空间、轮换令牌 |
| 命名空间令牌（建命名空间时返回） | **只**能写自己那个命名空间的服务与实例 |
| 不带令牌 | 读接口默认可用（便于平台拉取）；**写接口默认也开放**（见下） |

**写接口默认开放**（`REGISTRY_WRITE_AUTH=open`，本次按需求设定）：不带令牌即可登记/修改/删除。
代价很直白 —— 本服务是**唯一的元信息真源**，开放写入意味着**谁能连上谁就能投毒**（伪造服务地址/API）。
所以：

- 启动日志会打印 **WARN**，`/v1/meta` 暴露 `writeAuth`，面板右上角显示**黄色徽标**，不会悄悄开放；
- 开放 ≠ 不认令牌：带了**合法**令牌仍按身份记账（审计里能看到 `admin` / `ns:<name>`）；
  带了**非法**令牌一律 `403`（不静默忽略）；
- **收紧只需一步**：`backend/.env` 里把 `REGISTRY_WRITE_AUTH` 改成 `token` 再重启，
  客户端带 admin 或命名空间令牌即可 —— 令牌早就在 `.env` 里生成了，不用重新分发；
- 读接口可用 `REGISTRY_READ_AUTH=token` 单独收紧；`/health` `/healthz` `/readyz` `/metrics` 始终开放。
- 令牌只存 sha256 哈希，明文仅在创建/轮换时返回一次；服务绑定 `127.0.0.1`，不对外网暴露。
- `DELETE /v1/namespaces/{ns}?confirm={ns}` 才允许删除命名空间（连带其服务与实例）。

## API 一览

| Method | Path | 说明 | 鉴权 |
|---|---|---|---|
| PUT | `/v1/namespaces/{ns}/services/{svc}` | 登记/更新服务契约（含 api.spec 或 api.endpoints） | 写 |
| GET | `/v1/namespaces/{ns}/services/{svc}` | 契约详情（含端点索引与实例数） | 读 |
| DELETE | `/v1/namespaces/{ns}/services/{svc}` | 删除契约（连带实例与端点索引） | 写 |
| GET | `/v1/namespaces/{ns}/services/{svc}/spec` | 取回内联 OpenAPI 原文（`?download=1` 下载） | 读 |
| GET | `/v1/namespaces/{ns}/services/{svc}/instances` | 实例列表（`?pick=random`、`?meta=k=v`） | 读 |
| PUT | `/v1/namespaces/{ns}/services/{svc}/instances` | **声明式整组同步实例**（CI 首选） | 写 |
| POST | `/v1/namespaces/{ns}/services/{svc}/instances` | 登记单个实例 | 写 |
| PATCH/DELETE | `/v1/namespaces/{ns}/services/{svc}/instances/{id}` | 改/注销实例 | 写 |
| GET | `/v1/services` | 服务目录（`tag`/`owner`/`protocol`/`q`/分页） | 读 |
| GET | `/v1/instances/{id}` | 按实例 ID 全局查询 | 读 |
| GET | `/v1/search/apis` | 反查"这个接口谁提供"（exact/template/glob） | 读 |
| GET | `/v1/snapshot` | 全量快照（ETag / 304） | 读 |
| GET | `/v1/changes` | 增量游标（`since` / `wait` long-poll） | 读 |
| GET | `/v1/events` | SSE 实时变更流 | 读 |
| GET | `/v1/audit` | 审计查询（entity/ref/op 过滤） | 读 |
| GET/POST | `/v1/namespaces` | 命名空间列表 / 创建（创建需 admin） | 读 / admin |
| GET/PUT/DELETE | `/v1/namespaces/{ns}` | 读 / 改描述 / 删除（`?confirm={ns}`，需 admin） | 读 / admin |
| POST/DELETE | `/v1/namespaces/{ns}/token` | 轮换 / 清除注册令牌（需 admin） | admin |
| GET | `/health` `/healthz` `/readyz` `/metrics` `/v1/meta` | 运维 | 无 |
| GET | `/panel/` | 控制面板 | 无 |

完整契约见 [`api/openapi.yaml`](api/openapi.yaml)（这份契约本身也是可登记的：
本服务对自己的 API 描述与别人的服务一视同仁）。错误统一为
`{"error":{"code":"not_found|conflict|invalid_request|unauthorized|forbidden|internal","message":"..."}}`。

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `REGISTRY_HTTP_ADDR` | `127.0.0.1:$PORT`（PORT 默认 4240） | 监听地址 |
| `REGISTRY_BIND` | `127.0.0.1` | 未显式给地址时用的绑定 IP |
| `REGISTRY_DB_PATH` | `./data/registry.db` | SQLite 路径（`:memory:` 用于测试） |
| `REGISTRY_ADMIN_TOKEN` | 空* | admin 令牌；合法令牌会被记入审计（`actor=admin`） |
| `REGISTRY_WRITE_AUTH` | **`open`** | 写接口是否要令牌：`open`（默认，无需令牌） / `token`（需令牌） |
| `REGISTRY_READ_AUTH` | `open` | 读接口是否要令牌（`open` / `token`） |
| `REGISTRY_DEFAULT_NS` | `default` | 启动时自动播种的命名空间（空则不播种） |
| `REGISTRY_MAX_SPEC_BYTES` | `262144` | 单个服务内联 OpenAPI 原文大小上限 |
| `REGISTRY_PULL_DEFAULT_LIMIT` / `REGISTRY_PULL_MAX_LIMIT` | `100` / `1000` | 增量拉取分页 |
| `REGISTRY_PULL_WAIT_MAX` | `30s` | long-poll 挂起上限 |
| `REGISTRY_SSE_KEEPALIVE` | `20s` | SSE 保活间隔 |
| `REGISTRY_CHANGE_RETENTION_DAYS` | `0` | >0 时每日清理超期变更/审计记录；0=永久保留 |
| `REGISTRY_SHUTDOWN_TIMEOUT` | `10s` | 优雅退出等待 |
| `REGISTRY_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

\* 部署脚本 `scripts/start.sh` 首次启动会自动生成一个随机 `REGISTRY_ADMIN_TOKEN` 写进
`backend/.env`（0600）。写接口默认开放，因此**这个默认配置不要暴露到非可信网络**：
改成 `REGISTRY_WRITE_AUTH=token` 重启即可收紧。

非法取值会**启动即失败**（不会带着错误配置假装跑起来）。

## 部署

见 [`deploy/README.md`](deploy/README.md)：发版包由仓库根 `build.sh` 产出
（`outputs/bin/registryd` + `scripts/restart.sh`），运行期状态只放 `backend/`
（`.env` / `data/registry.db` / `runtime.pid` / `server.log`），由部署控制面
`:4220` 打包、部署、探活 `/health`。

> **不要把它自己登记进它自己**：本服务不探活、不心跳，自指数据没有意义；
> 它的存活由部署契约 + watchdog 负责。

## 开发

```bash
make lint         # gofmt 检查 + go vet
make test         # 全部单元测试（httptest 覆盖注册/发现/拉取/SSE/权限）
make race         # 竞态检测（并发写 + long-poll + SSE 是关键路径）
make build        # → bin/registryd
make run          # 本机起来试
```

代码结构：

```
cmd/registryd/        进程入口（配置、优雅退出、指标声明）
internal/api/         HTTP 路由、鉴权、DTO、校验、面板托管
internal/store/       SQLite 持久化（契约/实例/变更日志/审计/快照/端点检索）
internal/apispec/     内联 OpenAPI 解析（提取端点 + 基本校验 + sha256）
internal/model/       数据模型（JSON 与存储共用）
internal/notify/      提交后广播（long-poll / SSE 的唤醒原语）
internal/metrics/     极小的 Prometheus 文本暴露
internal/config/      环境变量配置（非法值即失败）
web/                  控制面板（原生 HTML/CSS/JS，go:embed 进二进制）
client/register.sh    一次性注册脚本（给 CI / 运维用）
deploy/               部署规范落地（platform 契约登记、Dockerfile、compose）
```

设计决策与理由（含被否决的方案）见 [`docs/DESIGN.md`](docs/DESIGN.md)。

