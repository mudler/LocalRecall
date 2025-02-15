package xlog

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
)

var logger *slog.Logger

func init() {
	var level = slog.LevelDebug
	if v, err := strconv.Atoi(os.Getenv("LOG_LEVEL")); err == nil {
		level = slog.Level(v)
	}
	var opts = &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	logger = slog.New(handler)
}

func _log(level slog.Level, msg string, args ...any) {
	_, f, l, _ := runtime.Caller(2)
	group := slog.Group(
		"source",
		slog.Attr{
			Key:   "filename",
			Value: slog.AnyValue(f),
		},
		slog.Attr{
			Key:   "lineno",
			Value: slog.AnyValue(l),
		},
	)
	args = append(args, group)
	logger.Log(context.Background(), level, msg, args...)
}

func Info(msg string, args ...any) {
	_log(slog.LevelInfo, msg, args...)
}

func Debug(msg string, args ...any) {
	_log(slog.LevelDebug, msg, args...)
}

func Error(msg string, args ...any) {
	_log(slog.LevelError, msg, args...)
}

func Warn(msg string, args ...any) {
	_log(slog.LevelWarn, msg, args...)
}
