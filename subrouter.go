package mqttkit

import (
	"context"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// SubContext 是订阅消息在洋葱模型（middleware 链）中的执行上下文。
// 注意：它不绑定具体协议字段，只提供消息/客户端与链路控制能力。
type SubContext struct {
	context.Context

	Client  mqtt.Client
	Message mqtt.Message

	handlers []SubHandler
	index    int
	aborted  bool

	pool *sync.Pool // 回收用；为 nil 表示无需回收
}

// Next 继续执行下一层 handler（middleware/最终 handler）。
// 典型用法：在 middleware 内部写 sc.Next()，并在其返回后写你希望的“后置逻辑”。
func (c *SubContext) Next() {
	for c.index < len(c.handlers) && !c.aborted {
		h := c.handlers[c.index]
		c.index++
		h(c)
	}
}

// Abort 终止后续链路，不再执行剩余 handlers。
func (c *SubContext) Abort() {
	c.aborted = true
}

// SubHandler 既可以表示 middleware，也可以表示最终业务 handler。
// middleware 通常会调用 c.Next()；最终 handler 可以不调用。
type SubHandler func(*SubContext)

type subRoute struct {
	topic string
	qos   byte
	chain []SubHandler
}

// SubEngine 负责：
// 1) 保存全局/分组 middleware；
// 2) 保存每条订阅 route 的 handler 链；
// 3) Register 时把每条 route 注册到 mqttkit.Client。
//
// 该组件不做 MQTT topic 匹配解析：topic 参数必须与调用方传给 Broker 的订阅过滤器一致。
type SubEngine struct {
	baseCtx context.Context

	mu     sync.Mutex
	global []SubHandler
	routes []subRoute
}

// NewSubEngine 创建新的订阅中间件引擎。
func NewSubEngine() *SubEngine { return &SubEngine{} }

// Use 注册全局 middleware（作用于所有路由）。
func (e *SubEngine) Use(mw ...SubHandler) *SubEngine {
	e.mu.Lock()
	e.global = append(e.global, mw...)
	e.mu.Unlock()
	return e
}

// Group 创建一个以 prefix 组织的路由分组（类似 gin.Group）。
// suffix 与 prefix 用 '/' 拼接成完整 topic。
func (e *SubEngine) Group(prefix string) *SubRouteGroup {
	return &SubRouteGroup{engine: e, prefix: prefix}
}

// Handle 直接注册一条路由（等价于 Group("").Handle）。
func (e *SubEngine) Handle(topic string, qos byte, handler SubHandler, mw ...SubHandler) *SubEngine {
	g := &SubRouteGroup{engine: e, prefix: ""}
	g.Handle(topic, qos, handler, mw...)
	return e
}

// Register 把 engine 里声明的所有路由注册到 client，并返回每条路由的 unsubscribe 闭包。
// 注册失败会回滚已成功注册的部分。
func (e *SubEngine) Register(ctx context.Context, c *Client) ([]func() error, error) {
	if c == nil {
		return nil, context.Canceled
	}
	e.baseCtx = ctx

	// 拷贝出 routes，避免 Register 与动态 Use/Handle 同时修改时出现竞态。
	e.mu.Lock()
	routes := make([]subRoute, len(e.routes))
	copy(routes, e.routes)
	e.mu.Unlock()

	if len(routes) == 0 {
		return nil, nil
	}

	unsubs := make([]func() error, 0, len(routes))
	for _, r := range routes {
		route := r
		unsub, err := c.Subscribe(ctx, route.topic, route.qos, func(cl mqtt.Client, msg mqtt.Message) {
			sc := &SubContext{
				Context:  e.baseCtx,
				Client:   cl,
				Message:  msg,
				handlers: route.chain,
				index:    0,
			}
			sc.Next()
		})
		if err != nil {
			for i := len(unsubs) - 1; i >= 0; i-- {
				_ = unsubs[i]()
			}
			return nil, err
		}
		unsubs = append(unsubs, unsub)
	}
	return unsubs, nil
}

// SubGroup 是 topic 前缀分组，middleware 只作用于该分组下的路由。
type SubRouteGroup struct {
	engine *SubEngine
	prefix string

	mu  sync.Mutex
	mws []SubHandler
}

// Use 注册分组级 middleware。
func (g *SubRouteGroup) Use(mw ...SubHandler) *SubRouteGroup {
	g.mu.Lock()
	g.mws = append(g.mws, mw...)
	g.mu.Unlock()
	return g
}

// Handle 在当前分组下注册一条路由：join(prefix, suffix) 作为订阅过滤器。
// 额外的 mw 参数会插入在分组 middleware 后、最终 handler 前。
func (g *SubRouteGroup) Handle(suffix string, qos byte, handler SubHandler, mw ...SubHandler) *SubRouteGroup {
	if handler == nil {
		panic("mqttkit: SubRouteGroup.Handle handler is nil")
	}

	topic := joinTopic(g.prefix, suffix)

	// 构建链：全局 middleware + 分组 middleware + route 额外 middleware + 最终 handler
	g.mu.Lock()
	groupMws := append([]SubHandler(nil), g.mws...)
	g.mu.Unlock()

	var chain []SubHandler
	g.engine.mu.Lock()
	chain = append(chain, g.engine.global...)
	g.engine.mu.Unlock()
	chain = append(chain, groupMws...)
	chain = append(chain, mw...)
	chain = append(chain, handler)

	g.engine.mu.Lock()
	g.engine.routes = append(g.engine.routes, subRoute{topic: topic, qos: qos, chain: chain})
	g.engine.mu.Unlock()

	return g
}
