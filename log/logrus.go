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

func ToLogrus(l *Logger, opts ...LogrusOpt) *logrus.Logger {
	opt := logrusOpt{}
	for _, o := range opts {
		o(&opt)
	}
	logger := logrus.New()

	// format all the logrus logs using zerolog
	logger.SetFormatter(logrusFormatter{l, opt})

	// crank the log level to the max so all logs are passed to zerolog
	logger.SetLevel(logrus.TraceLevel)

	return logger
}

func ToLogrusFormatter(l *Logger, opts ...LogrusOpt) logrus.Formatter {
	opt := logrusOpt{}
	for _, o := range opts {
		o(&opt)
	}
	return logrusFormatter{l, opt}
}

type logrusOpt struct {
	drop map[string]struct{}
}

type LogrusOpt func(*logrusOpt)

func WithLogrusDroppedFields(fields ...string) LogrusOpt {
	return func(o *logrusOpt) {
		if o.drop == nil {
			o.drop = make(map[string]struct{})
		}
		for _, f := range fields {
			o.drop[f] = struct{}{}
		}
	}
}

type logrusFormatter struct {
	l   *Logger
	opt logrusOpt
}

func (f logrusFormatter) Format(entry *logrus.Entry) ([]byte, error) {
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
		if _, ok := f.opt.drop[k]; ok {
			continue
		}
		e = e.Any(k, v)
	}

	// log here
	e.Msg(entry.Message)

	// drop the entry
	return nil, nil
}
