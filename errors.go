package mqttkit

import "errors"

var (
	ErrClosed        = errors.New("mqttkit: client closed")
	ErrNotConnected  = errors.New("mqttkit: not connected")
	ErrQueueFull     = errors.New("mqttkit: publish queue full")
	ErrInvalidOption = errors.New("mqttkit: invalid option")
)
