// Package mqttkit 提供线程安全的 MQTT 客户端封装（基于 Eclipse Paho）。
//
// 特性概要：
//   - 单后台 goroutine 处理连接与订阅表，避免与 Paho 回调并发改写客户端状态导致数据竞争。
//   - 重连成功后自动按当前订阅表重新 Subscribe。
//   - 未连接时 Publish 可走有界队列；PublishSync 仅在线时等待发送完成。
//   - 分布式互斥仅提供 DistributedLocker 接口与 WithLock 辅助函数，具体实现（Redis/etcd 等）由业务按自身中间件选型。
//
// Topic/报文解析不在本库；请在业务或独立协议模块实现。
package mqttkit

import (
	"context"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Client 是对 Paho 客户端的薄封装：内部用命令通道串行化对连接与订阅的修改。
type Client struct {
	opts Options

	// 发往 runLoop 的订阅/发布/取消等命令
	cmdCh chan any

	cancel context.CancelFunc

	wg sync.WaitGroup

	closeOnce sync.Once
	// closed 在 runLoop 退出时关闭，用于快速判断 ErrClosed
	closed chan struct{}

	firstOnce    sync.Once
	firstConnect chan struct{} // 首次 OnConnect 成功时关闭，供 WaitConnected 使用
}

// NewClient 创建客户端并启动内部 runLoop。ctx 取消或调用 Close 将结束循环并断开连接。
// 传入的 Options 会经 normalize 填充默认值；若 BrokerURLs 为空返回错误。
func NewClient(ctx context.Context, o Options) (*Client, error) {
	cp := o
	if err := cp.normalize(); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		opts:         cp,
		cmdCh:        make(chan any, cp.CmdQueueCap),
		cancel:       cancel,
		closed:       make(chan struct{}),
		firstConnect: make(chan struct{}),
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(c.closed)
		c.runLoop(loopCtx)
	}()
	return c, nil
}

// Close 取消内部上下文并等待 runLoop 退出；可重复调用，仅首次生效。
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
	})
	c.wg.Wait()
	return nil
}

// signalFirstConnect 在首次 Paho OnConnect 回调时关闭 firstConnect（仅一次）。
func (c *Client) signalFirstConnect() {
	c.firstOnce.Do(func() { close(c.firstConnect) })
}

// WaitConnected 阻塞直到第一次成功连接（首次 OnConnect），或 ctx 取消/超时。
// 若从未连上且 ctx 未设时限，可能长期阻塞，上层应使用带超时的 ctx。
func (c *Client) WaitConnected(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.firstConnect:
		return nil
	}
}

// errIfClosed 若客户端已关闭或 ctx 已取消则返回相应错误。
func (c *Client) errIfClosed(ctx context.Context) error {
	select {
	case <-c.closed:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Subscribe 注册 topic 与回调；重连后会在 OnConnect 中自动再次 Subscribe。
//
// 返回的 unsubscribe 调用后会从订阅表移除并在已连接时向 Broker 发送 Unsubscribe。
//
// 注意：handler 可能由 Paho 的网络 goroutine 调用，应快速返回或将工作投递到自有 worker。
func (c *Client) Subscribe(ctx context.Context, topic string, qos byte, handler mqtt.MessageHandler) (unsubscribe func() error, err error) {
	if err = c.errIfClosed(ctx); err != nil {
		return nil, err
	}
	resCh := make(chan subscribeResult, 1)
	select {
	case <-c.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case c.cmdCh <- cmdSubscribe{topic: topic, qos: qos, handler: handler, resCh: resCh}:
	}
	select {
	case <-c.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resCh:
		return r.unsub, r.err
	}
}

// unsubscribe 向 runLoop 投递取消订阅命令（由 Subscribe 返回的闭包调用）。
func (c *Client) unsubscribe(topic string) error {
	resCh := make(chan error, 1)
	select {
	case <-c.closed:
		return ErrClosed
	case c.cmdCh <- cmdUnsubscribe{topic: topic, resCh: resCh}:
	}
	select {
	case <-c.closed:
		return ErrClosed
	case err := <-resCh:
		return err
	}
}

// Publish 异步发布：已连接则立即 Publish；否则将 payload 拷贝后排入离线队列（最长 PublishQueueSize），
// 连上后在 OnConnect 处理中刷出。不等待 Broker 级 Paho token 完成。
func (c *Client) Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	return c.publish(ctx, topic, qos, retained, payload, false)
}

// PublishSync 同步发布：等待 Paho token 在超时内完成。若当前未连接，返回 ErrNotConnected（不入队）。
func (c *Client) PublishSync(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	return c.publish(ctx, topic, qos, retained, payload, true)
}

// publish 内部统一投递发布命令；wait 为 true 时表示 PublishSync。
func (c *Client) publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte, wait bool) error {
	if err := c.errIfClosed(ctx); err != nil {
		return err
	}
	p := append([]byte(nil), payload...)
	resCh := make(chan error, 1)
	cmd := cmdPublish{topic: topic, qos: qos, retained: retained, payload: p, wait: wait, resCh: resCh}
	select {
	case <-c.closed:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case c.cmdCh <- cmd:
	}
	select {
	case <-c.closed:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case err := <-resCh:
		return err
	}
}
