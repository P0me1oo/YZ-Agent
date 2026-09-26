// Package logrotate 限制日志文件大小，防止长期运行后占满磁盘。
//
// 适用于程序输出被重定向到普通文件的情况，例如 OpenRC 的 output_log/error_log，
// 以及配置文件里指定的日志文件路径。systemd（journald）和 Docker 的输出不是
// 普通文件，不受影响。
//
// 超过上限时把旧文件改名为 .1，再将程序持有的日志输出切换到新文件。
// 改名到切换之间的写入仍留在 .1，不会因截断而丢失。
package logrotate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

const (
	// DefaultMaxBytes 是单个日志文件的上限。
	DefaultMaxBytes int64 = 20 << 20
	// DefaultKeep 是保留的旧日志份数（.1 到 .3）。
	DefaultKeep = 3
	// checkInterval 是检查文件大小的间隔。
	checkInterval = time.Minute
)

// Rotator 维护当前需要轮转的日志路径，配置热重载时可整体替换。
type Rotator struct {
	mu       sync.RWMutex
	files    []string
	wake     chan struct{}
	done     chan struct{}
	maxBytes int64
	keep     int
}

// Start 在后台定期检查给定日志文件，超过上限时轮转。
// 初始路径为空时仍保持运行，以便热重载后加入新的日志文件。
func Start(ctx context.Context, paths ...string) *Rotator {
	return start(ctx, checkInterval, DefaultMaxBytes, DefaultKeep, paths...)
}

func start(ctx context.Context, interval time.Duration, maxBytes int64, keep int, paths ...string) *Rotator {
	r := &Rotator{
		files: uniqueFiles(paths), wake: make(chan struct{}, 1), done: make(chan struct{}),
		maxBytes: maxBytes, keep: keep,
	}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			r.rotate()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-r.wake:
			}
		}
	}()
	return r
}

// Update 在配置热重载后替换日志路径，并立即检查新路径。
func (r *Rotator) Update(paths ...string) {
	r.mu.Lock()
	r.files = uniqueFiles(paths)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Rotator) rotate() {
	r.mu.RLock()
	files := r.files
	r.mu.RUnlock()
	for _, path := range files {
		rotated, err := RotateIfLarger(path, r.maxBytes, r.keep)
		if err != nil {
			nlog.Core().Warn("log rotation failed", "path", path, "error", err)
		} else if rotated {
			nlog.Core().Info("log file rotated", "path", path)
		}
	}
}

// RotateIfLarger 在文件超过 maxBytes 时轮转，返回是否发生了轮转。文件不存在时不做任何事。
func RotateIfLarger(path string, maxBytes int64, keep int) (bool, error) {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() <= maxBytes {
		return false, nil
	}
	if keep < 1 {
		keep = 1
	}
	fds := stdoutFDs(path)
	rotate := func() (*os.File, error) {
		if err := rotateBackups(path, keep); err != nil {
			return nil, err
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
		next, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, info.Mode().Perm())
		if err != nil {
			return nil, err
		}
		for _, fd := range fds {
			if err := rebindStdout(fd, next); err != nil {
				_ = next.Close()
				return nil, err
			}
		}
		return next, nil
	}
	if owned, err := nlog.RotateFile(path, rotate); owned {
		return err == nil, err
	}
	if len(fds) == 0 {
		// 不是程序持有的输出文件，无法安全切换仍在写入的文件描述符。
		return false, nil
	}
	next, err := rotate()
	if err != nil {
		return false, err
	}
	defer next.Close()
	return true, nil
}

func rotateBackups(path string, keep int) error {
	last := fmt.Sprintf("%s.%d", path, keep)
	if err := os.Remove(last); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, fmt.Sprintf("%s.%d", path, i+1)); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func uniqueFiles(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
