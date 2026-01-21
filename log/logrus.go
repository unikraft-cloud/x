// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"errors"

	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

func ToLogrus(l *Logger) *logrus.Logger {
	logger := logrus.New()
	// format all the logrus logs using zerolog
	logger.SetFormatter(logFormatter{l})
	// crank the log level to the max so all logs are passed to zerolog
	logger.SetLevel(logrus.TraceLevel)
	return logger
}

type logFormatter struct {
	l *Logger
}

func (f logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var e *zerolog.Event

	switch entry.Level {
	case logrus.TraceLevel:
		e = f.l.Trace()
	case logrus.DebugLevel:
		e = f.l.Debug()
	case logrus.InfoLevel:
		e = f.l.Info()
	case logrus.WarnLevel:
		e = f.l.Warn()
	case logrus.ErrorLevel:
		e = f.l.Error()
	case logrus.FatalLevel:
		e = f.l.Error() // nothing should be fatal
	default:
		return nil, errors.New("unknown log level")
	}

	for k, v := range entry.Data {
		switch k {
		// drop some fields
		case "go.version":
		default:
			e = e.Any(k, v)
		}
	}

	// log here
	e.Msg(entry.Message)

	// drop the entry
	return nil, nil
}
