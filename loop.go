package mqttkit

import (
	"context"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type subscription struct {
	topic   string
	qos     byte
	handler mqtt.MessageHandler
}

type publishJob struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

type loopState struct {
	mu        sync.Mutex
	subs      map[string]*subscription
	pending   []publishJob
	maxPen    int
	mqttCli   mqtt.Client
	connected bool
}

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

type cmdUnsubscribe struct {
	topic string
	resCh chan error
}

type cmdPublish struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
	wait     bool
	resCh    chan error
}

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

func (c *Client) handleSubscribe(st *loopState, m cmdSubscribe) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.subs[m.topic] = &subscription{topic: m.topic, qos: m.qos, handler: m.handler}
	if st.mqttCli != nil && st.mqttCli.IsConnected() {
		tok := st.mqttCli.Subscribe(m.topic, m.qos, m.handler)
		if ok := tok.WaitTimeout(30 * time.Second); ok && tok.Error() != nil {
			delete(st.subs, m.topic)
			m.resCh <- subscribeResult{err: tok.Error()}
			return
		}
	}
	topic := m.topic
	m.resCh <- subscribeResult{
		unsub: func() error { return c.unsubscribe(topic) },
		err:   nil,
	}
}

func (c *Client) handleUnsubscribe(st *loopState, m cmdUnsubscribe) {
	st.mu.Lock()
	delete(st.subs, m.topic)
	cli := st.mqttCli
	conn := st.connected
	st.mu.Unlock()

	var err error
	if cli != nil && conn {
		tok := cli.Unsubscribe(m.topic)
		if ok := tok.WaitTimeout(30 * time.Second); ok {
			err = tok.Error()
		}
	}
	m.resCh <- err
}

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

func (c *Client) doPublish(cli mqtt.Client, m cmdPublish) {
	tok := cli.Publish(m.topic, m.qos, m.retained, m.payload)
	if !m.wait {
		if m.resCh != nil {
			m.resCh <- nil
		}
		return
	}
	var err error
	if ok := tok.WaitTimeout(30 * time.Second); ok {
		err = tok.Error()
	}
	if m.resCh != nil {
		m.resCh <- err
	}
}

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

		for _, s := range subs {
			tok := cl.Subscribe(s.topic, s.qos, s.handler)
			if ok := tok.WaitTimeout(30 * time.Second); ok && tok.Error() != nil {
				c.opts.Logger.Printf("mqttkit: subscribe %s: %v", s.topic, tok.Error())
			}
		}
		for _, job := range pending {
			_ = cl.Publish(job.topic, job.qos, job.retained, job.payload)
		}
		c.signalFirstConnect()
	})
	return o
}
