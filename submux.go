package mqttkit

import (
	"context"
	"fmt"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type subEntry struct {
	topic   string
	qos     byte
	handler mqtt.MessageHandler
}

// SubMux 是一个订阅注册器，用于把多个订阅像 Gin 路由一样“先声明、再统一 Register”。
//
// 设计目标：
//  1. 提高可读性：把订阅从调用点逻辑里抽离出来；
//  2. 可组合：支持 Group(prefix) 以减少重复拼接 Topic；
//  3. 失败可回滚：Register 过程中若某个订阅失败，会撤销之前已注册的订阅。
//
// 注意：mqttkit 内部用 topic 字符串作为订阅键（同 topic 只能保留一条 handler），因此同一个 topic
// 不应注册多次。
type SubMux struct {
	entries []subEntry
}

// NewSubMux 创建一个新的订阅注册器。
func NewSubMux() *SubMux { return &SubMux{} }

// Handle 注册一个完整 topic 订阅（不立即生效）。
func (m *SubMux) Handle(topic string, qos byte, handler mqtt.MessageHandler) *SubMux {
	if topic == "" {
		panic("mqttkit: SubMux.Handle topic is empty")
	}
	if handler == nil {
		panic("mqttkit: SubMux.Handle handler is nil")
	}
	m.entries = append(m.entries, subEntry{topic: topic, qos: qos, handler: handler})
	return m
}

// Group 基于 prefix 创建分组，suffix 将与 prefix 拼接成完整 topic。
func (m *SubMux) Group(prefix string) *SubGroup {
	return &SubGroup{mux: m, prefix: prefix}
}

// Register 将 mux 里声明的所有订阅注册到 client，并返回对应的 unsubscribe 闭包列表。
// 若中途注册失败，会回滚之前已成功注册的订阅。
func (m *SubMux) Register(ctx context.Context, c *Client) ([]func() error, error) {
	if c == nil {
		return nil, fmt.Errorf("mqttkit: SubMux.Register client is nil")
	}
	if len(m.entries) == 0 {
		return nil, nil
	}

	unsubs := make([]func() error, 0, len(m.entries))
	for _, e := range m.entries {
		unsub, err := c.Subscribe(ctx, e.topic, e.qos, e.handler)
		if err != nil {
			// 回滚已注册部分，尽力而为。
			for i := len(unsubs) - 1; i >= 0; i-- {
				_ = unsubs[i]()
			}
			return nil, err
		}
		unsubs = append(unsubs, unsub)
	}
	return unsubs, nil
}

// SubGroup 表示一个 topic 前缀分组（类似 Gin 的 router.Group）。
type SubGroup struct {
	mux    *SubMux
	prefix string
}

// Handle 在该分组下注册 topic：join(prefix, suffix)。
func (g *SubGroup) Handle(suffix string, qos byte, handler mqtt.MessageHandler) *SubGroup {
	topic := joinTopic(g.prefix, suffix)
	g.mux.Handle(topic, qos, handler)
	return g
}

func joinTopic(prefix, suffix string) string {
	prefix = strings.TrimSpace(prefix)
	suffix = strings.TrimSpace(suffix)
	if prefix == "" {
		return suffix
	}
	if suffix == "" {
		return prefix
	}
	// 保证类似 /a/b + c -> /a/b/c
	prefix = strings.TrimSuffix(prefix, "/")
	suffix = strings.TrimPrefix(suffix, "/")
	return prefix + "/" + suffix
}
