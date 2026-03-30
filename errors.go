package mqttkit

import "errors"

var (
	// ErrClosed 表示 Client 已关闭（Close 后或 runLoop 已退出），不应再发布/订阅。
	ErrClosed = errors.New("mqttkit: client closed")
	// ErrNotConnected 表示 PublishSync 在未连接时被调用（同步发送不入离线队列）。
	ErrNotConnected = errors.New("mqttkit: not connected")
	// ErrQueueFull 表示离线异步发布队列已满（超过 PublishQueueSize）。
	ErrQueueFull = errors.New("mqttkit: publish queue full")
	// ErrInvalidOption 表示 Options 无效，例如 BrokerURLs 为空。
	ErrInvalidOption = errors.New("mqttkit: invalid option")
)
