package logger

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type LocalEvent struct {
	*zerolog.Event
}

func NewLocalEvent(event *zerolog.Event) *LocalEvent {
	return &LocalEvent{event}
}

func (l *LocalEvent) Msg(format string, args ...interface{}) {
	l.Msgf(format, args...)
}

func (l *LocalEvent) Err(err error) Event {
	return NewLocalEvent(log.Err(err))
}

type LocalLogger struct {
}

func (l LocalLogger) Info() Event {
	return NewLocalEvent(log.Info())
}

func (l LocalLogger) Debug() Event {
	return NewLocalEvent(log.Debug())
}

func (l LocalLogger) Warn() Event {
	return NewLocalEvent(log.Warn())
}

func (l LocalLogger) Error() Event {
	return NewLocalEvent(log.Error())
}
