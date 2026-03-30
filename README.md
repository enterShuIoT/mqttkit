# mqttkit

基于 [Paho MQTT](https://github.com/eclipse/paho.mqtt.golang) 的 Go 客户端封装：**单 runLoop 串行处理命令**，Broker 侧的 **`Subscribe` / `Unsubscribe` / `PublishSync` 的长时间等待放到独立 goroutine**，避免一条慢请求占死整条命令队列；重连后对多条订阅**并行** `Subscribe` 以缩短窗口；支持有界离线发布队列。

本库**不包含** Topic/报文解析；请在业务或独立协议模块实现。

## 引入模块

```text
github.com/enterShuIoT/mqttkit
```

本地替换示例：

```go
replace github.com/enterShuIoT/mqttkit => ./pkg/mqttkit
```

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

// 像 Gin 一样先注册，再统一 Register。
unsubs, err := mqttkit.NewSubMux().
    Handle("demo/#", 1, func(_ mqtt.Client, m mqtt.Message) {
        log.Printf("%s %s", m.Topic(), m.Payload())
    }).
    Register(ctx, c)
if err != nil {
    log.Fatal(err)
}
defer func() {
    for _, u := range unsubs {
        _ = u()
    }
}()

if err := c.Publish(ctx, "demo/hello", 1, false, []byte("hi")); err != nil {
    log.Fatal(err)
}
```

> 订阅回调可能在 Paho 网络路径上执行，请尽快返回；重逻辑投递到自有 worker。

## 订阅洋葱模型（可选）SubEngine

如果你希望把通用逻辑（例如统一耗时统计、统一日志、幂等/加锁编排）写成 middleware，并形成“洋葱链”，可以使用 `mqttkit.SubEngine`：

```go
unsubs, err := mqttkit.NewSubEngine().
	Use(func(sc *mqttkit.SubContext) {
		start := time.Now()
		sc.Next()
		log.Printf("handled %s cost=%s", sc.Message.Topic(), time.Since(start))
	}).
	Handle("demo/#", 1, func(sc *mqttkit.SubContext) {
		log.Printf("%s %s", sc.Message.Topic(), sc.Message.Payload())
	}).
	Register(ctx, c)
if err != nil {
	log.Fatal(err)
}
defer func() {
	for _, u := range unsubs {
		_ = u()
	}
}()
```

## 常用配置（Options）

| 字段 | 说明 |
|------|------|
| `BrokerURLs` | Broker 地址，可多段或在单字符串内用英文逗号分隔 |
| `ClientID` / `Username` / `Password` | 连接身份 |
| `TLS` | `*tls.Config`；也可用 `mqttkit.TLSConfigFromPEM("ca.pem")` |
| `ConnectRetry` / `ConnectTimeout` / `MaxReconnectInterval` | 连接与重连节奏 |
| `AutoReconnect` | `*bool`，`nil` 时默认开启自动重连 |
| `CleanSession` | 是否干净会话 |
| `PublishQueueSize` | 未连接时异步 `Publish` 的排队上限 |
| `CmdQueueCap` | 发往 runLoop 的命令通道容量，默认 **2048**（突发大时可调大） |
| `Logger` | 诊断输出，nil 则静默 |

## 发布：`Publish` 与 `PublishSync`

- **`Publish`**：已连接则立即 `Publish`（不等待 token）；未连接则入队，连上后刷出。
- **`PublishSync`**：在后台等待 Paho token；**调用方仍要等结果**，但 **runLoop 不会被这次等待占住**，其它 Subscribe/Publish 可继续进队处理。未连接时返回 `ErrNotConnected`。

单一 **TCP 连接 + 单 Paho Client** 的吞吐仍受 Broker 与 Paho 限制；`PublishSync` 会额外起 goroutine 等 token。海量网关若共用一个 Client，通常还需水平扩展多连接或由 Broker/架构侧分流。

## 分布式锁（仅接口）

多实例服务共享订阅时，可用 **`mqttkit.DistributedLocker`** + **`WithLock`** / **`ConsumerPartitionKey`** 对「同一网关/设备」的处理做互斥。**本库不提供 Redis/etcd 等任何实现**，由业务按自有中间件实现接口后在 handler 里注入使用。

```go
var locker mqttkit.DistributedLocker = newRedisLocker(...) // 业务实现；nil 表示不加锁

mux := mqttkit.NewSubMux().
    Handle("device/+/data", 1, func(_ mqtt.Client, m mqtt.Message) {
        deviceID := parseDeviceID(m.Topic())
        key := mqttkit.ConsumerPartitionKey("acme", instanceID, deviceID)
        _ = mqttkit.WithLock(ctx, locker, key, func(ctx context.Context) error {
            return handleOnce(ctx, m)
        })
    })
_, _ = mux.Register(ctx, c)
```

## 错误变量

| 变量 | 含义 |
|------|------|
| `ErrClosed` | 客户端已关闭 |
| `ErrNotConnected` | `PublishSync` 在未连接时 |
| `ErrQueueFull` | 离线异步队列已满 |
| `ErrInvalidOption` | 如未配置 `BrokerURLs` |

## 开发与测试

```bash
CGO_ENABLED=0 go test ./...
```

## 依赖

- Go 1.21+
- `github.com/eclipse/paho.mqtt.golang`
