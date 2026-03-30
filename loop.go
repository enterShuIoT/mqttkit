package mqttkit

import (
	"context"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// subscription 表示一条主题订阅（topic + QoS + Paho 回调）。
type subscription struct {
	topic   string
	qos     byte
	handler mqtt.MessageHandler
}

// publishJob 离线队列中的待发消息（已断开连接时的异步 Publish）。
type publishJob struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

// loopState 由 runLoop 与 Paho 回调通过 mu 协调；长时间网络等待放到独立 goroutine，避免占死 runLoop。
type loopState struct {
	mu        sync.Mutex
	subs      map[string]*subscription
	pending   []publishJob
	maxPen    int
	mqttCli   mqtt.Client
	connected bool
}

// cmdSubscribe 发往 runLoop 的订阅命令。
type cmdSubscribe struct {
	topic   string
	qos     byte
	handler mqtt.MessageHandler
	resCh   chan subscribeResult
}

type subscribeResult struct {
	unsub func() error
	err   error
}

// cmdUnsubscribe 取消订阅命令。
type cmdUnsubscribe struct {
	topic string
	resCh chan error
}

// cmdPublish 发布命令；wait 为 true 时对应 PublishSync（结果在后台 goroutine 写入 resCh，不阻塞 runLoop）。
type cmdPublish struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
	wait     bool
	resCh    chan error
}

// runLoop 串行处理命令；Broker 侧的阻塞等待在独立 goroutine 中完成，避免「一台慢操作拖住全局 cmdCh」。
func (c *Client) runLoop(ctx context.Context) {
	st := &loopState{
		subs:   make(map[string]*subscription),
		maxPen: c.opts.PublishQueueSize,
	}
	pahoOpts := c.buildPahoOptions(st)
	cli := mqtt.NewClient(pahoOpts)
	st.mqttCli = cli

	go func() {
		tok := cli.Connect()
		if ok := tok.WaitTimeout(c.opts.ConnectTimeout); ok && tok.Error() != nil {
			c.opts.Logger.Printf("mqttkit: connect: %v", tok.Error())
		}
	}()

	for {
		select {
		case <-ctx.Done():
			st.disconnect()
			return
		case cmd, ok := <-c.cmdCh:
			if !ok {
				st.disconnect()
				return
			}
			switch m := cmd.(type) {
			case cmdSubscribe:
				c.handleSubscribe(st, m)
			case cmdUnsubscribe:
				c.handleUnsubscribe(st, m)
			case cmdPublish:
				c.handlePublish(st, m)
			}
		}
	}
}

func (st *loopState) disconnect() {
	st.mu.Lock()
	cli := st.mqttCli
	st.connected = false
	st.mu.Unlock()
	if cli != nil && cli.IsConnected() {
		cli.Disconnect(250)
	}
}

// handleSubscribe 更新订阅表；若需向 Broker Subscribe，在后台 goroutine 内 Wait，避免阻塞 runLoop。
func (c *Client) handleSubscribe(st *loopState, m cmdSubscribe) {
	topic, qos, handler := m.topic, m.qos, m.handler
	resCh := m.resCh

	st.mu.Lock()
	st.subs[topic] = &subscription{topic: topic, qos: qos, handler: handler}
	cli := st.mqttCli
	conn := st.connected
	st.mu.Unlock()

	if cli != nil && conn {
		go func() {
			tok := cli.Subscribe(topic, qos, handler)
			var err error
			if ok := tok.WaitTimeout(30 * time.Second); ok {
				err = tok.Error()
			}
			if err != nil {
				st.mu.Lock()
				delete(st.subs, topic)
				st.mu.Unlock()
				resCh <- subscribeResult{err: err}
				return
			}
			resCh <- subscribeResult{
				unsub: func() error { return c.unsubscribe(topic) },
				err:   nil,
			}
		}()
		return
	}
	resCh <- subscribeResult{
		unsub: func() error { return c.unsubscribe(topic) },
		err:   nil,
	}
}

// handleUnsubscribe 从表删除；Broker Unsubscribe 在后台 goroutine 等待完成。
func (c *Client) handleUnsubscribe(st *loopState, m cmdUnsubscribe) {
	topic := m.topic
	resCh := m.resCh

	st.mu.Lock()
	delete(st.subs, topic)
	cli := st.mqttCli
	conn := st.connected
	st.mu.Unlock()

	if cli != nil && conn {
		go func() {
			var err error
			tok := cli.Unsubscribe(topic)
			if ok := tok.WaitTimeout(30 * time.Second); ok {
				err = tok.Error()
			}
			resCh <- err
		}()
		return
	}
	resCh <- nil
}

// handlePublish 处理发布；已连接时异步路径由 doPublish 内部决定是否起 goroutine。
func (c *Client) handlePublish(st *loopState, m cmdPublish) {
	st.mu.Lock()
	cli := st.mqttCli
	conn := st.connected
	if conn && cli != nil && cli.IsConnected() {
		st.mu.Unlock()
		c.doPublish(cli, m)
		return
	}
	if m.wait {
		st.mu.Unlock()
		if m.resCh != nil {
			m.resCh <- ErrNotConnected
		}
		return
	}
	if len(st.pending) >= st.maxPen {
		st.mu.Unlock()
		if m.resCh != nil {
			m.resCh <- ErrQueueFull
		}
		return
	}
	payload := append([]byte(nil), m.payload...)
	st.pending = append(st.pending, publishJob{
		topic: m.topic, qos: m.qos, retained: m.retained, payload: payload,
	})
	st.mu.Unlock()
	if m.resCh != nil {
		m.resCh <- nil
	}
}

// doPublish：异步 Publish 立即向 resCh 返回；PublishSync 在 goroutine 内 WaitTimeout，不阻塞 runLoop。
func (c *Client) doPublish(cli mqtt.Client, m cmdPublish) {
	if !m.wait {
		_ = cli.Publish(m.topic, m.qos, m.retained, m.payload)
		if m.resCh != nil {
			m.resCh <- nil
		}
		return
	}
	topic, qos, retained, payload := m.topic, m.qos, m.retained, m.payload
	resCh := m.resCh
	go func() {
		tok := cli.Publish(topic, qos, retained, payload)
		var err error
		if ok := tok.WaitTimeout(30 * time.Second); ok {
			err = tok.Error()
		}
		if resCh != nil {
			resCh <- err
		}
	}()
}

// buildPahoOptions 组装 Paho ClientOptions。OnConnect 内对多条订阅并行 Subscribe+Wait，缩短重连后窗口。
func (c *Client) buildPahoOptions(st *loopState) *mqtt.ClientOptions {
	o := mqtt.NewClientOptions()
	for _, u := range c.opts.BrokerURLs {
		o.AddBroker(u)
	}
	if c.opts.ClientID != "" {
		o.SetClientID(c.opts.ClientID)
	}
	if c.opts.Username != "" {
		o.SetUsername(c.opts.Username)
	}
	if c.opts.Password != "" {
		o.SetPassword(c.opts.Password)
	}
	if c.opts.TLS != nil {
		o.SetTLSConfig(c.opts.TLS)
	}
	o.SetAutoReconnect(*c.opts.AutoReconnect)
	o.SetConnectRetryInterval(c.opts.ConnectRetry)
	o.SetMaxReconnectInterval(c.opts.MaxReconnectInterval)
	o.SetCleanSession(c.opts.CleanSession)
	o.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		st.mu.Lock()
		st.connected = false
		st.mu.Unlock()
		c.opts.Logger.Printf("mqttkit: connection lost: %v", err)
	})
	o.SetOnConnectHandler(func(cl mqtt.Client) {
		st.mu.Lock()
		st.connected = true
		subs := make([]*subscription, 0, len(st.subs))
		for _, s := range st.subs {
			subs = append(subs, s)
		}
		pending := st.pending
		st.pending = st.pending[:0]
		st.mu.Unlock()

		var wg sync.WaitGroup
		for _, s := range subs {
			s := s
			wg.Add(1)
			go func() {
				defer wg.Done()
				tok := cl.Subscribe(s.topic, s.qos, s.handler)
				if ok := tok.WaitTimeout(30 * time.Second); ok && tok.Error() != nil {
					c.opts.Logger.Printf("mqttkit: subscribe %s: %v", s.topic, tok.Error())
				}
			}()
		}
		wg.Wait()

		for _, job := range pending {
			_ = cl.Publish(job.topic, job.qos, job.retained, job.payload)
		}
		c.signalFirstConnect()
	})
	return o
}
