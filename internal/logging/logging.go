package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(service string) *slog.Logger {
	logger := newLogger(service, os.Getenv("LOG_FORMAT"), os.Stdout)
	slog.SetDefault(logger)
	return logger
}

func newLogger(service, format string, output io.Writer) *slog.Logger {
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "text") {
		handler = slog.NewTextHandler(output, &slog.HandlerOptions{})
	} else {
		handler = slog.NewJSONHandler(output, &slog.HandlerOptions{})
	}
	return slog.New(handler).With("service", service)
}
