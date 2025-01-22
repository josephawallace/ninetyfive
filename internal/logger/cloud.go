package logger

import (
	"fmt"

	"cloud.google.com/go/logging"
)

type CloudEvent struct {
	severity logging.Severity
	err      error
	logger   *CloudLogger
}

func NewCloudEvent(logger *CloudLogger, severity logging.Severity, err error) *CloudEvent {
	return &CloudEvent{
		severity: severity,
		err:      err,
		logger:   logger,
	}
}

func (ce *CloudEvent) Msg(format string, args ...interface{}) {
	ce.logger.client.Logger(name).StandardLogger(ce.severity).Println(fmt.Sprintf(format, args...))
	if ce.err != nil {
		ce.logger.client.Logger(name).StandardLogger(ce.severity).Println(ce.err.Error())
	}
}

func (ce *CloudEvent) Err(err error) Event {
	ce.err = err
	return ce
}

type CloudLogger struct {
	client *logging.Client
}

func (l CloudLogger) Info() Event {
	return NewCloudEvent(&l, logging.Info, nil)
}

func (l CloudLogger) Debug() Event {
	return NewCloudEvent(&l, logging.Debug, nil)
}

func (l CloudLogger) Warn() Event {
	return NewCloudEvent(&l, logging.Warning, nil)
}

func (l CloudLogger) Error() Event {
	return NewCloudEvent(&l, logging.Error, nil)
}
