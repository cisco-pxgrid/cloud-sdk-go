// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

// Package log provides logging functionality for the SDK
package log

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime"
)

// SDKLogger defines the logging interface that can be implemented by the application
type SDKLogger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Debugf(format string, args ...interface{})
}

// Logger is used by the SDK for logging internal messages, can be overridden by the application
var Logger SDKLogger = &DefaultLogger{
	Level: LogLevelDebug,
}

type (
	// LogLevel defines the logging level
	LogLevel int
	// DefaultLogger implements the default logger used by the SDK
	DefaultLogger struct {
		Level LogLevel
	}
)

const (
	LogLevelDebug = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError
)

func getLogPrefix() string {
	var fileline string
	_, file, line, ok := runtime.Caller(2)
	if ok {
		dirname := filepath.Base(filepath.Dir(file))
		filename := filepath.Base(file)
		fileline = filepath.Join(dirname, filename)
	}
	return fmt.Sprintf("%s:%d", fileline, line)
}

// Infof logs info level messages
func (d *DefaultLogger) Infof(format string, args ...interface{}) {
	if LogLevelInfo >= d.Level {
		log.Printf("|  INFO | "+getLogPrefix()+" | "+format, args...)
	}
}

// Debugf logs debug level messages
func (d *DefaultLogger) Debugf(format string, args ...interface{}) {
	if LogLevelDebug >= d.Level {
		log.Printf("| DEBUG | "+getLogPrefix()+" | "+format, args...)
	}
}

// Errorf logs error level messages
func (d *DefaultLogger) Errorf(format string, args ...interface{}) {
	if LogLevelError >= d.Level {
		log.Printf("| ERROR | "+getLogPrefix()+" | "+format, args...)
	}
}

// Warnf logs warning level messages
func (d *DefaultLogger) Warnf(format string, args ...interface{}) {
	if LogLevelWarning >= d.Level {
		log.Printf("|  WARN | "+getLogPrefix()+" | "+format, args...)
	}
}
