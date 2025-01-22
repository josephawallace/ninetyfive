package logger

import (
	"cloud.google.com/go/logging"
)

const (
	name = "nflogs"
)

type Event interface {
	Msg(format string, args ...interface{})
	Err(err error) Event
}

type Logger interface {
	Debug() Event
	Info() Event
	Warn() Event
	Error() Event
}

func NewLogger(client *logging.Client) Logger {
	if client == nil {
		return LocalLogger{}
	}
	return CloudLogger{client}
}
