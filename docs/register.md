# 服务注册规范

给要接入 service-registry 的服务看。注册中心地址是 `http://127.0.0.1:4240`。

注册中心只存元信息。它不扫端口、不探活、不拉你的 OpenAPI。地址由服务自己在**开始监听之后**用 HTTP 报上来。

Go 服务也走这条 HTTP。不要为登记再接一套 Go `net/rpc` 或 gRPC。服务自己对外可以是 HTTP，也可以是 gRPC；那是契约上的 `api.protocols`，和这次上报无关。

## 两件事要分开

| | 契约 | 监听地址 |
|---|---|---|
| 是什么 | 你是谁：命名空间、服务名、对外 API、owner、部门、仓库 | 你现在听在哪：scheme、host、port |
| 什么时候做 | 接口定下来或发版时做一次 | 进程 `Listen` 成功之后做一次 |
| 接口 | `PUT /v1/namespaces/{ns}/services/{svc}` | `PUT /v1/namespaces/{ns}/services/{svc}/announce` |
| 谁来做 | CI / `client/register.sh`（Go 可走 `client/ci/register-go-service.sh`） | 服务进程自己 |

契约不存在时，上报地址会 **404**，正文会要求先登记契约。部署平台上的 `serviceId` 就是路径里的服务名 `{svc}`。`{ns}` 用这份契约已经登记的命名空间。

名字只允许小写字母、数字和 `.` `_` `-`，以字母或数字开头和结尾，最长 63 个字符。

## 进程起来后上报地址

监听成功后发一次：

```http
PUT /v1/namespaces/{ns}/services/{svc}/announce
Content-Type: application/json

{"port": 9099, "os": "linux"}
```

本机：

```bash
curl -sS -X PUT "http://127.0.0.1:4240/v1/namespaces/${NS}/services/${SERVICE}/announce" \
  -H 'content-type: application/json' \
  -d "{\"port\":${PORT},\"os\":\"linux\"}"
```

`PORT` 用进程真正在听的端口。部署平台注入的 `SERVICE_PORT` 就是这个值。

### 请求体

| 字段 | 必填 | 说明 |
|---|---|---|
| `port` | 是 | 1–65535，实际监听端口 |
| `os` | 是 | 这台机器的系统，只接受 `linux` / `mac` / `windows`（大小写不敏感，落库为小写） |
| `host` | 否 | 省略时用这次 TCP 连接的对端地址。本机连 `127.0.0.1:4240` 记下来就是 `127.0.0.1`。前面有代理、对端地址不对时再显式填写。只写主机名或 IP，不要带 `http://` 和路径。IPv6 写成 `[addr]` |
| `scheme` | 否 | 省略为 `http`。可选 `http` / `https` / `tcp` / `tls` |
| `arch` | 否 | `amd64` / `arm64` / `386` / `arm`。`x86_64` 会收成 `amd64`，`aarch64` 会收成 `arm64`。不传则保留库里已有的值 |
| `metadata` | 否 | 字符串键值，最多 64 条，键 ≤128、值 ≤1024。不传则保留库里已有的值；传了就会整份替换 |

Go 的 `runtime.GOOS` 在 macOS 上是 `darwin`，上报前要改成 `mac`。`darwin` 会被拒。

### Go 示例

```go
func announce(ctx context.Context, ns, service string, port int) error {
    osName := runtime.GOOS
    if osName == "darwin" {
        osName = "mac"
    }
    body, err := json.Marshal(map[string]any{"port": port, "os": osName})
    if err != nil {
        return err
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodPut,
        "http://127.0.0.1:4240/v1/namespaces/"+ns+"/services/"+service+"/announce",
        bytes.NewReader(body))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return fmt.Errorf("announce %s: %s", resp.Status, b)
    }
    return nil
}
```

放在 `Listen` 成功、开始接请求之前。失败应让启动失败，避免进程在跑、目录里却没有这条地址。

### 返回

| 状态 | 含义 |
|---|---|
| 201 | 这条 `(scheme, host, port)` 是新建的。`created` 为 `true` |
| 200 | 同一地址已存在。内容没变时只刷新 `lastAnnouncedAt`，`updatedAt` 和 `revision` 不动 |
| 400 | 端口、os、host 不合法，或请求体为空 |
| 401 | 写接口已收紧且没带令牌 |
| 403 | 令牌无效，或令牌不属于这个命名空间 |
| 404 | 命名空间或服务契约不存在 |

成功体是 `{"instance":{...},"created":true|false}`。错误体是 `{"error":{"code":"...","message":"..."}}`。

同一地址再报一次不会删掉这个服务的其他地址。内容（os / arch / metadata）变了才会推进 `revision`，并记一条变更。

## 不要做成心跳

报一次即可。注册中心没有 TTL，也不会因为你不再调用就把地址摘掉。反复调用只是把 `lastAnnouncedAt` 往后推，不表示健康。

消费方要确认能不能用，自己请求契约里的 `healthPath`。注册中心不返回健康状态。

进程退出后地址还在。要在退出时摘掉，用响应里的 `instance.id`：

```http
DELETE /v1/namespaces/{ns}/services/{svc}/instances/{id}
```

## 进程里不要调用整组同步

`PUT /v1/namespaces/{ns}/services/{svc}/instances` 是声明式的：请求里没出现的地址会被删掉。它留给 CI 或部署流水线对齐一整组实例。进程启动只调用 `announce`。

`client/ci/register-go-service.sh` 的 `INSTANCES` 走的就是整组同步。进程已经自己上报之后，不要再用一份不含这条地址的列表去同步，否则刚报上的地址会被摘掉。

## 别人怎么找到你

```http
GET /v1/namespaces/{ns}/services/{svc}/instances
```

返回该服务登记过的全部实例。`?pick=random` 随机拿一条。`?meta=zone=edge` 按 metadata 过滤。没有“可用”字段。

## 鉴权

当前写接口默认开放（`REGISTRY_WRITE_AUTH=open`），上报可以不带令牌。`GET /v1/meta` 的 `writeAuth` 为 `open` 时就是这个状态。

收紧为 `token` 之后，带命名空间令牌：

```http
Authorization: Bearer <token>
```

或 `X-Registry-Token: <token>`。命名空间令牌只能写自己的命名空间。带了非法令牌会 403，不会被当成匿名放行。

## 契约登记（上报地址之前）

接口变更走 CI，不在进程启动时做。Go 服务用 swag 注解生成规范，再交给：

```bash
SERVICE_NAME=event-center REGISTRY_NS=default \
  DEPARTMENT_ID=D0005 INSTANCES=127.0.0.1:9099 OWNER=kaulie HEALTH_PATH=/health \
  bash client/ci/register-go-service.sh
```

细节见 [client/ci/README.md](../client/ci/README.md)。机器可读的接口定义在 [api/openapi.yaml](../api/openapi.yaml) 的 `PUT /v1/namespaces/{ns}/services/{svc}/announce`。
