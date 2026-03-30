package mqttkit

import "log"

// Logger receives diagnostic messages from Client.
type Logger interface {
	Printf(format string, v ...any)
}

type nopLogger struct{}

func (nopLogger) Printf(format string, v ...any) {}

// StdLogger adapts the standard library log to Logger.
type StdLogger struct{}

func (StdLogger) Printf(format string, v ...any) {
	log.Printf(format, v...)
}
