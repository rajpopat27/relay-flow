// Package logging configures the process-wide slog logger for serve.
// Task 9.1: --debug serve flag OR RELAY_FLOW_LOG_LEVEL env (flag wins);
// default info. Output goes to server.log AND stderr, stdlib slog only.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options control Setup. Debug is the --debug serve flag; Env is the value
// of RELAY_FLOW_LOG_LEVEL (read once by the caller for testability).
// Flag wins over env.
type Options struct {
	Debug bool
	Env   string
}

// Setup installs the process-wide slog logger writing to logPath AND
// stderr. Effective level: --debug flag → debug; else RELAY_FLOW_LOG_LEVEL
// (debug|info|warn|error, case-insensitive); else info. Unknown env values
// are ignored (info). The returned closer flushes/closes the log file; it
// is safe to call more than once.
func Setup(logPath string, o Options) (io.Closer, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open server log %s: %w", logPath, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chmod server log %s: %w", logPath, err)
	}
	level := levelFor(o)
	h := slog.NewTextHandler(io.MultiWriter(f, os.Stderr), &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	return f, nil
}

func levelFor(o Options) slog.Level {
	if o.Debug {
		return slog.LevelDebug
	}
	switch strings.ToLower(strings.TrimSpace(o.Env)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
