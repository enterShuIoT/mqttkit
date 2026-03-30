package mqttkit

import "log"

// Logger 为 Client 提供的诊断输出接口（与标准库 log.Printf 形态一致）。
type Logger interface {
	Printf(format string, v ...any)
}

// nopLogger 空实现，不输出任何内容。
type nopLogger struct{}

func (nopLogger) Printf(format string, v ...any) {}

// StdLogger 将日志转发到标准库 log.Printf。
type StdLogger struct{}

func (StdLogger) Printf(format string, v ...any) {
	log.Printf(format, v...)
}
