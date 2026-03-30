// Package mqttkit provides a concurrency-safe MQTT client: resubscribe on reconnect,
// bounded offline publishing, and an optional DistributedLocker for multi-instance
// gateways that share subscriptions (dedupe handling so one logical gateway owns a device’s traffic).
package mqttkit

import (
	"context"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Client is a thin, mutex-safe wrapper: one goroutine owns the Paho client and subscription map.
type Client struct {
	opts Options

	cmdCh chan any

	cancel context.CancelFunc

	wg sync.WaitGroup

	closeOnce sync.Once
	closed    chan struct{}

	firstOnce    sync.Once
	firstConnect chan struct{}
}

// NewClient starts the background loop; ctx cancels the client (same as Close).
func NewClient(ctx context.Context, o Options) (*Client, error) {
	cp := o
	if err := cp.normalize(); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		opts:         cp,
		cmdCh:        make(chan any, 256),
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

// Close cancels the context and waits for the loop to exit.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
	})
	c.wg.Wait()
	return nil
}

func (c *Client) signalFirstConnect() {
	c.firstOnce.Do(func() { close(c.firstConnect) })
}

// WaitConnected blocks until the first OnConnect success or ctx is done.
func (c *Client) WaitConnected(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.firstConnect:
		return nil
	}
}

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

// Subscribe registers handler for topic; replayed automatically after reconnect.
// Handler may be invoked from Paho's network goroutine — keep it fast or offload work.
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

// Publish enqueues a publish. When disconnected, payloads are queued up to PublishQueueSize.
func (c *Client) Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	return c.publish(ctx, topic, qos, retained, payload, false)
}

// PublishSync waits for the broker to accept the message (Paho token completion).
func (c *Client) PublishSync(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	return c.publish(ctx, topic, qos, retained, payload, true)
}

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

// Locker returns the optional distributed locker from Options (may be nil).
func (c *Client) Locker() DistributedLocker {
	return c.opts.Locker
}
