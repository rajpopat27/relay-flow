package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Both foreground and the in-process CLI seam write the same owned record.
// PID lets the detached parent distinguish its child from a racing lock owner.
func logStartupFailure(err error) {
	slog.Error("server startup failed", "pid", os.Getpid(), "error", startupDiagnostic(err.Error()))
}

func startupLogOffset(path string) int64 {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		return -1 // Cannot establish this attempt's boundary: do not replay logs.
	}
	return info.Size()
}

// readStartupFailure reads only this attempt's bounded tail, and only the
// selected child's owned startup-failure record. All other log content stays
// in server.log. Missing/truncated/unreadable records leave the normal exit
// diagnostic and log location intact, never substitute an older error.
func readStartupFailure(path string, offset int64, pid int) string {
	if offset < 0 || pid <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return ""
	}
	const maxDiagnosticBytes int64 = 64 << 10
	start := max(offset, info.Size()-maxDiagnosticBytes)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDiagnosticBytes))
	if err != nil {
		return ""
	}
	// logging.Setup owns this text-handler record and its field order.
	marker := fmt.Sprintf(`msg="server startup failed" pid=%d error=`, pid)
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		index := strings.Index(lines[i], marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(lines[i][index+len(marker):])
		if strings.HasPrefix(value, `"`) {
			value, err = strconv.Unquote(value)
			if err != nil {
				return ""
			}
		}
		return startupDiagnostic(value)
	}
	return ""
}

// Credential redaction belongs to the adapter. This final display step keeps
// external control characters out of both foreground and detached caller TUIs.
func startupDiagnostic(message string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
}
