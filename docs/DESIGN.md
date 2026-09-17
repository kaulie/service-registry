# 设计说明

本文记录 service-registry 的**定位、语义、协议设计与被否决的方案**。
需求原文只有四条：① 服务注册中心 ② 提供服务的全局注册和发现能力
③ 对外提供注册、查询接口 ④ 独立的控制面板。

## 1. 定位：唯一真源 + 拉模型

```
 各服务 / CI / 部署流水线 ──PUT(一次性)──▶  service-registry  ◀──pull──  部署控制面 / watchdog / 网关 / 其他平台
```

需求评审阶段定了三条边界（都是"减法"，直接决定了本设计的形态）：

| 决策 | 后果 |
|---|---|
| **本服务是全局唯一、独立的注册中心；其他平台从这里拉取服务元信息** | ① 不做"从部署控制面 `:4220` / watchdog `:4230` 拉数据"的连接器——方向相反；② 必须把**给平台拉取**的契约做成一等公民（快照 / 增量游标 / 实时），而不是附属接口 |
| **各服务不需要和本平台维持心跳** | 没有 lease/TTL、没有续约接口、没有常驻 SDK。登记是**一次性幂等声明** |
| **注册中心不需要主动探活，它只是元信息的存储中心** | 没有探针、没有状态机、没有"up/down"。实例没有任何运行时状态位 |

于是"服务的健康/可用"这件事在本设计里**被显式排除**。这不是缺失，而是一个语义承诺：

> 本中心返回的是**登记事实**（"谁声明了它存在、它的接口是什么"），
> 不是**运行时事实**（"它现在活着吗"）。可达性由消费方自行校验；
> 契约里的 `healthPath`、`gitRepoUrl` 就是给消费方（或 watchdog / 人）用的元信息字段
> —— 本中心只存不碰（不 clone 仓库、不发任何请求）。

**因此本服务只剩两件事要做扎实**：① 元信息模型要表达完整（尤其是"服务的基础属性"——
对外 API）；② 元信息要能被可靠地、可增量地拉走。

## 2. 数据模型

```
Namespace（隔离域）
  name · description · token_hash · created_at · updated_at
   └─ Service（契约，低频：发版时变）
        identity : namespace · name · version · owner · git_repo_url(仓库地址，仅元信息) · tags[] · description
        access   : basePath · healthPath(仅元信息) · docsUrl
        api      : protocols[] · authSchemes[] · specUrl · spec(内联原文) · specHash
                   · endpoints[]（method/path/summary/operationId/tags/auth）
        audit    : revision · created_at · updated_at · registered_by
      └─ Instance（声明，无状态位）
           id · scheme · host · port · metadata{} · revision
           · registered_at · updated_at · registered_by
```

### 2.1 为什么"对外 API"是 Service 的属性而不是 Instance 的

一个服务的对外接口是**发版时确定、与实例无关**的事实：3 个副本的 API 是同一套。
把它放在实例上会产生 N 份重复且必然不同步的数据，也会让"这个接口谁提供"变成
一次去重查询。放在 Service 上之后：

- 契约一次登记，实例怎么加都不影响 API 描述；
- 反查（`/v1/search/apis`）只需要扫 `endpoints` 表；
- 面板能按服务展示"端点表 + 用真实实例拼出的调用示例"。

### 2.2 API 的两种来源

| 来源 | 行为 |
|---|---|
| `api.spec`（内联 OpenAPI 3.x / Swagger 2.0 原文，YAML 或 JSON） | 服务端解析出端点索引、算 `specHash`、原样保存原文（可经 `.../spec` 取回）。解析失败即 400，不落库 |
| `api.endpoints`（显式端点列表） | 直接校验并入库（method 合法、path 以 `/` 开头、不重复），不产生 spec 原文 |

两者都给时**显式端点优先**，spec 仍然保存（供 `specHash`/原文取回）。
两者都没有 → 400：**"对外 API 是服务的基础属性，不允许为空"**。

`specUrl` 只存不抓取：抓取会引入出网、SSRF 与定时任务，且让"登记"变成"运行"。
需要外部规范时由消费方按 `specUrl` 自己去取。

### 2.3 实例身份与声明式同步

- 身份：显式 `id`（`inst_<ULID>`，字典序即时间序）或 `(scheme,host,port)` 的唯一约束。
- `PUT .../instances` 是**声明式**的：传期望集合，服务端 diff 出 create/update/delete，
  多余的一律摘除。CI/部署流水线调一次就完事，不需要任何常驻进程 ——
  这也正是"不需要心跳"之后，实例集合该怎么维护的答案。
- 匹配顺序：先按 `id`，再按 `(scheme,host,port)`。所以"换了地址但没带 id"会被记成
  "删旧 + 建新"（诚实且安全）；想表达"同一个实例换地址"，带上原 `id` 即可。

## 3. 变更日志：一份数据，三种用法

每个写操作在一个事务里额外写一条 `changes` 记录：

```
changes(revision PK AUTOINCREMENT, at, namespace, entity, ref, op, actor, detail)
entities 各自记录自己最后一次被改的 revision
```

由此同一份日志同时支撑三件事：

| 用法 | 接口 | 语义 |
|---|---|---|
| 全量快照 | `GET /v1/snapshot` | 返回当前 `revision`；ETag = `W/"<revision>-<ns|all>"`，可直接 304 |
| 增量游标 | `GET /v1/changes?since=<R>` | 返回 `revision > R` 的记录；`wait=30s` 为 long-poll |
| 实时订阅 | `GET /v1/events`（SSE） | 先发 `hello`(当前 revision)，再逐条 `change` |

**三者一致性**：它们读同一张表、同一个 `revision` 序列，所以"快照里没有的、
增量里也不会冒出来"。消费方可以放心地长期轮询，也可以用 ETag 省流量。

变更记录也是**审计**（`actor` 记录 `admin` / `ns:<name>` / `anonymous`），
`GET /v1/audit` 就是同一张表的过滤视图。保留策略由
`REGISTRY_CHANGE_RETENTION_DAYS` 控制（0 = 永久保留，因为审计本身有价值）。

### 3.1 并发与唤醒

- SQLite + WAL，写事务用 `_txlock=immediate`（先拿写锁再干活，避免锁升级导致的
  `SQLITE_BUSY`）；`busy_timeout=5000`。
- 写提交**之后**才对 `notify.Hub` 广播（关闭当前 channel 再换一个新的，经典唤醒原语）：
  被唤醒的读者立刻查库一定能看到刚提交的数据。
- long-poll / SSE 都是"醒来 → 按游标查库"，而不是"从内存队列收事件"，
  因此**不会丢事件**，也不会因为连接慢而积压内存。

## 4. 端点检索：exact / template / glob

`GET /v1/search/apis?method=&path=&match=` 回答"这个接口谁提供"。三种匹配：

| 模式 | 例子 | 说明 |
|---|---|---|
| `exact` | 查询 `/v1/streams/{stream}/events` | 与登记的路径字面完全一致 |
| `template` | 查询 `/v1/streams/abc/events`，登记的是 `/v1/streams/{stream}/events` | 把登记的模板编译成正则（`{param}` → `[^/]+`）去匹配具体路径 |
| `glob` | 查询 `/v1/streams/*/events` 或 `/v1/**` | `*` 匹配单段（不含 `/`）、`**` 跨段 |

默认顺序 exact → template → glob，同类内更具体的（路径更长的）排前面；
`match=` 可强制指定一种（可预测性）。查询里出现 `*`/`**`/`?`/`{x}` 时视为"模式"，
不再按"具体路径 vs 登记模板"解释。

通配的实现有个小技巧：把登记路径里的 `{param}` 换成字面 `*` 作为**探针串**，
再把查询编译成正则去匹配探针串 —— 段通配 `[^/]+` 恰好也能匹配那个 `*` 字符，
于是 `/v1/streams/*/events` 命中 `/v1/streams/{stream}/events`，`/v1/**` 命中任意路径。
（测试 `TestSearchEndpointsMatching` 覆盖了全部组合，包括"强制 exact 时不回退"。）

## 5. 错误语义

```json
{"error":{"code":"not_found","message":"服务 event-center不存在"}}
```

| 状态 | code | 触发 |
|---|---|---|
| 400 | `invalid_request` | 字段校验失败、缺少 API 来源、规格无法解析、`pick` 取值不支持、未带 `confirm` 删命名空间 |
| 401 | `unauthorized` | 写接口没带令牌（或读接口被配置为需令牌时没带） |
| 403 | `forbidden` | 令牌无效 / 命名空间令牌越权 / 非 admin 做管理操作 |
| 404 | `not_found` | 对象不存在（含"服务未登记契约就登记实例"，提示里给出该先调哪个接口） |
| 405 | `method_not_allowed` | 路径存在但方法不对（带 `Allow` 头） |
| 409 | `conflict` | 同名命名空间/服务已存在、实例地址重复 |
| 500 | `internal` | 数据层/序列化异常（同时计入 `registry_errors_total`） |

约束：命名空间/服务名走 DNS-label 风格（小写字母数字与 `._-`，≤63）——
将来若要映射成网关路由或域名，不需要再做转换；host 不接受带协议/路径；
port ∈ [1,65535]；metadata ≤64 条、键 ≤128/值 ≤1024 字节。

## 6. 安全模型

- **写**：`Authorization: Bearer <t>` 或 `X-Registry-Token`；令牌只存 sha256，
  比较用 `subtle.ConstantTimeCompare`。admin 令牌全权；命名空间令牌只能写自己那个命名空间。
- **写接口默认开放**（`REGISTRY_WRITE_AUTH=open`，按需求设定）：不带令牌也能登记/维护/管理，
  `actor=anonymous`。这是**刻意的默认值**（本机/内网先把链路跑通），但必须显式可见：
  启动打 WARN、`/v1/meta.writeAuth` 暴露、面板显示黄色徽标。收紧是一步操作
  （`.env` 改 `token` 后重启），令牌无需重新分发。
  开放不等于不认令牌：带合法令牌按身份记账（审计里能区分 `admin`/`ns:<name>`），
  带**非法**令牌一律 403 —— 不静默忽略一个错误的令牌。
  风险要说清楚：本服务是**唯一真源**，写接口一旦暴露给不可信网络，就等于允许任何人
  **向全局发现结果里投毒**（伪造地址/API）。所以绑定默认是 `127.0.0.1`。
- **读**：默认开放（`REGISTRY_READ_AUTH=open`），因为消费方是多个平台；
  需要收紧时改为 `token`。运维端点（`/health` `/healthz` `/readyz` `/metrics`）始终开放。
- `DELETE /v1/namespaces/{ns}?confirm={ns}`：破坏性操作要求显式确认。
- 单条内联 spec ≤ `REGISTRY_MAX_SPEC_BYTES`（默认 256KB），请求体硬上限 1MB，
  端点数上限 2000，单次实例同步上限 500 —— 都是为了防止畸形输入把库撑爆。

## 7. 被否决的方案（以及为什么）

| 方案 | 否决理由 |
|---|---|
| 客户端心跳 + TTL/lease 续约 | 决策："各服务不需要和本平台维持心跳"。心跳要求每个服务带常驻客户端、处理网络抖动、实现续约退避；而这些复杂度换来的只是"可能过期的注册状态" |
| 注册中心主动探活 + up/down 状态机 | 决策："注册中心不需要主动探活，它只是元信息的存储中心"。且与 watchdog 职责重叠：探活→状态机的真源应在监控侧，本中心只提供元信息供其消费 |
| 从部署控制面 `:4220` / watchdog `:4230` 拉数据做"聚合" | 决策："其他平台从本中心拉取"——方向相反。若真要做聚合，应该是部署方在部署时**写**给我们（一次 PUT），而不是我们定时去别人那里爬 |
| `pick=round_robin` / `pick=weighted` | 轮次是**每个消费方各自的状态**。一个被多个消费方共用的注册中心无法提供正确的共享轮次（A 选完，B 的轮次被吃掉）；权重则需要额外的人工维护却没有探活数据支撑。只留无状态的 `pick=random`，其余让消费方自己做 |
| `enabled`（人工摘流开关） | 决策：去掉。没有探活的前提下它只是"另一份需要人工同步的状态"；要摘流就 DELETE 实例，或由消费方按 metadata 过滤 |
| 自动抓取 `specUrl` 做变更检测/兼容性告警 | 引入出网与定时任务（SSRF 面、外部依赖），且与"存储中心"的定位不符。`specHash` 已足够让消费方自己做 diff |
| 独立前端工程（React/Vite） | 面板只需要"列表 + 表格 + 表单 + SSE"，原生 HTML/CSS/JS 零构建步骤、`go:embed` 进单二进制，部署时不存在"前端资源没跟上"的问题（与生态里部署面板的做法一致） |
| 多节点集群 / 选主 / federation | 定位是**唯一真源**的单实例服务；引入分布式一致性会显著放大复杂度，而当前收益为零 |
| 写接口默认要求令牌 | 需求方要求"先默认开放写权限"：唯一的真源上开放写入本身是风险（等于允许投毒），但先把链路跑通更重要。折中做法是**开放但显式可见**（启动 WARN + `/v1/meta.writeAuth` + 面板黄徽标），并把收紧做成**一个 flag**（`REGISTRY_WRITE_AUTH=token`，令牌早已生成、无需重新分发），而不是靠"删掉令牌"这种不可逆的手法 |

## 8. 路线图（M2+，都未实现）

1. **远端暴露**：cloud-server（nginx + systemd + `service-registry.<ip>.sslip.io`），
   复用同一份 `build.sh` 产物（`REGISTRY_BUILD_LINUX=1`）。
2. **优雅重启**：实现 `restartNotifyUrl` / `restartPollUrl`，部署时不丢在途请求
   （让部署控制面支持 graceful restart；本服务是只读为主，影响有限）。
3. **历史与差异**：`GET /v1/services/{ns}/{svc}/revisions`（按 revision 取历史契约快照）、
   spec 变更 diff（breaking change 提示）。
4. **导出/导入**：`GET /v1/export`（整体 JSON dump）与 `POST /v1/import`（迁移/备份）。
5. **变更保留与归档策略**：按命名空间配置；冷数据归档到文件而不是删掉。
6. **多租户收紧**：按命名空间的读权限、令牌 scope（只读令牌）、审计导出。
7. **消费方 SDK**：最小 Go/Node 客户端（快照 + 增量游标 + 本地缓存），
   让"拉取"变成几行代码；`client/register.sh` 已经覆盖了写入侧。

