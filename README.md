# mqttkit

基于 [Paho MQTT](https://github.com/eclipse/paho.mqtt.golang) 的 Go MQTT 客户端封装：连接与订阅状态在内部统一排队处理，重连后自动重订阅；支持有界离线发送队列；可选**分布式锁**接口，用于多实例接入网关共享订阅时的**防重复消费**。

本库**不包含**具体行业协议（如报文 JSON、Topic 命名规则）；协议解析与示例可在业务项目或独立 demo 模块中实现。子包 `proto` 仅提供与 MQTT Topic 解析相关的通用类型。

## 引入模块

模块路径以 `go.mod` 为准：

```text
github.com/enterShuIoT/mqttkit
```

在业务仓库中使用本地路径时，可在 `go.mod` 中增加：

```go
replace github.com/enterShuIoT/mqttkit => ./pkg/mqttkit
```

然后：

```bash
go get github.com/enterShuIoT/mqttkit
```

## 最小示例

```go
ctx := context.Background()
c, err := mqttkit.NewClient(ctx, mqttkit.Options{
    BrokerURLs: []string{"tcp://127.0.0.1:1883"},
    ClientID:   "my-client",
    Username:   "user",
    Password:   "pass",
})
if err != nil {
    log.Fatal(err)
}
defer c.Close()

if err := c.WaitConnected(ctx); err != nil {
    log.Fatal(err)
}

unsub, err := c.Subscribe(ctx, "demo/#", 1, func(_ mqtt.Client, m mqtt.Message) {
    log.Printf("%s %s", m.Topic(), m.Payload())
})
if err != nil {
    log.Fatal(err)
}
defer unsub()

if err := c.Publish(ctx, "demo/hello", 1, false, []byte("hi")); err != nil {
    log.Fatal(err)
}
```

> 订阅回调可能在 Paho 的网络协程中执行，请尽快返回；重逻辑请丢到自有 worker 或带缓冲 channel。

## 常用配置（Options）

| 字段 | 说明 |
|------|------|
| `BrokerURLs` | Broker 地址，可多段或在单字符串内用英文逗号分隔 |
| `ClientID` / `Username` / `Password` | 连接身份 |
| `TLS` | `*tls.Config`；也可用 `mqttkit.TLSConfigFromPEM("ca.pem")` |
| `ConnectRetry` / `ConnectTimeout` / `MaxReconnectInterval` | 连接与重连节奏，零值会在 `normalize()` 中改为合理默认 |
| `AutoReconnect` | `*bool`，`nil` 等价于默认开启自动重连 |
| `CleanSession` | 是否干净会话 |
| `PublishQueueSize` | 未连接时异步 `Publish` 的排队上限（超限返回 `ErrQueueFull`） |
| `Locker` | 可选；**Client 不会自动使用**，仅在业务 handler 中与 `WithLock` 等配合 |
| `Logger` | 诊断输出，nil 则静默 |

取消传入 `NewClient` 的 `ctx` 或调用 `Close()` 会结束后台循环并断开连接。

## 发布：`Publish` 与 `PublishSync`

- **`Publish`**：异步投递；已连接则立即发往 broker；未连接则复制 payload 后排入队列，连上后在 `OnConnect` 后尽快刷出（不等待 Paho token）。
- **`PublishSync`**：等待 Paho token 完成；**若当前未连接**，返回 `ErrNotConnected`（不会入队）。

payload 在内部会拷贝，调用方可在返回后安全复用切片。

## 多实例网关与分布式锁

多个网关副本若订阅**相同或重叠**的 Topic，同一条消息可能被多个实例各收一次。若希望**同一设备的业务只在一台上处理**（或避免重复写库、重复下发），可在消息处理前按「租户前缀 + 网关副本 ID + 设备 ID」等方式生成锁键，并实现 `mqttkit.DistributedLocker`（Redis、etcd、数据库 advisory lock 等；本库不提供具体实现）。

```go
locker := myredis.NewLocker(...) // 项目内实现 mqttkit.DistributedLocker

opts := mqttkit.Options{
    BrokerURLs: []string{"tcp://broker:1883"},
    ClientID:   "gw-az1-001",
    Locker:     locker,
}
c, _ := mqttkit.NewClient(ctx, opts)

_, _ = c.Subscribe(ctx, "device/+/data", 1, func(_ mqtt.Client, m mqtt.Message) {
    deviceID := parseDeviceID(m.Topic()) // 业务解析
    key := mqttkit.ConsumerPartitionKey("acme", opts.ClientID, deviceID)

    _ = mqttkit.WithLock(ctx, c.Locker(), key, func(ctx context.Context) error {
        return handleOnce(ctx, m) // 仅持锁实例执行核心业务
    })
})
```

- **`ConsumerPartitionKey(prefix, gatewayID, deviceID)`**：便于统一键格式；也可完全自建字符串。
- **`WithLock(ctx, locker, key, fn)`**：`locker == nil` 时直接执行 `fn`，便于单测或单实例部署。

## 子包 `proto`

`github.com/enterShuIoT/mqttkit/proto` 提供：

- `Route`：解析后的 Topic 语义片段（由业务定义各字段含义）。
- `TopicParser`：接口，具体产品协议在**业务仓库**中实现。

## 错误变量

| 变量 | 含义 |
|------|------|
| `ErrClosed` | 客户端已关闭 |
| `ErrNotConnected` | `PublishSync` 在未连接时 |
| `ErrQueueFull` | 离线异步队列已满 |
| `ErrInvalidOption` | 如未配置 `BrokerURLs` |

## 开发与测试

若本机执行 `go test` 时出现与动态链接相关的异常，可尝试：

```bash
CGO_ENABLED=0 go test ./...
```

## 依赖

- Go 1.21+
- `github.com/eclipse/paho.mqtt.golang`
