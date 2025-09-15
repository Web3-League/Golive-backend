// api/pkg/logger/logger.go
package logger

import (
	"fmt"
	"log/slog"
	"os"

	"golive/internal/config"
)

// Logger wraps slog.Logger with additional functionality
type Logger struct {
	*slog.Logger
}

// New creates a new logger instance
func New(cfg config.LoggingConfig) (*Logger, error) {
	var handler slog.Handler
	
	opts := &slog.HandlerOptions{
		Level: parseLevel(cfg.Level),
		AddSource: true,
	}
	
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	
	logger := slog.New(handler)
	
	return &Logger{Logger: logger}, nil
}

// WithFields creates a new logger with additional fields
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &Logger{Logger: l.Logger.With(args...)}
}

// With creates a new logger with additional key-value pairs
func (l *Logger) With(args ...interface{}) *Logger {
	return &Logger{Logger: l.Logger.With(args...)}
}

// Sync flushes any buffered log entries (no-op for slog)
func (l *Logger) Sync() error {
	return nil
}

// parseLevel converts string level to slog.Level
func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Convenience methods for backwards compatibility
func (l *Logger) Infof(format string, args ...interface{}) {
	l.Info(fmt.Sprintf(format, args...))
}

func (l *Logger) Errorf(format string, args ...interface{}) {
	l.Error(fmt.Sprintf(format, args...))
}

func (l *Logger) Debugf(format string, args ...interface{}) {
	l.Debug(fmt.Sprintf(format, args...))
}

func (l *Logger) Warnf(format string, args ...interface{}) {
	l.Warn(fmt.Sprintf(format, args...))
}

func (l *Logger) Fatal(msg string, args ...interface{}) {
	l.Error(msg, args...)
	os.Exit(1)
}