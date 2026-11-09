package nlog

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Failures limits each failing operation to one warning per minute. A successful
// retry resets it, so a subsequent failure is visible immediately.
type Failures struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (f *Failures) Report(logger *NodeLog, operation string, err error) {
	f.report(logger, operation, err, false)
}

// Local avoids feeding failures of the log uploader back into its own queue.
func (f *Failures) Local(logger *NodeLog, operation string, err error) {
	f.report(logger, operation, err, true)
}

func (f *Failures) report(logger *NodeLog, operation string, err error, local bool) {
	f.mu.Lock()
	if err == nil {
		delete(f.last, operation)
		f.mu.Unlock()
		return
	}
	if LogsDisabled() || time.Since(f.last[operation]) < time.Minute {
		f.mu.Unlock()
		return
	}
	if f.last == nil {
		f.last = make(map[string]time.Time)
	}
	f.last[operation] = time.Now()
	f.mu.Unlock()
	if logger == nil {
		logger = Core()
	}
	args := []any{"error_type", fmt.Sprintf("%T", err), "error", Sanitize(err.Error())}
	if local {
		logWithColor(slog.LevelWarn, logger.prefix, operation, args...)
	} else {
		logger.Warn(operation, args...)
	}
}
