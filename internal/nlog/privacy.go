package nlog

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var logsDisabled atomic.Bool
var policyMu sync.Mutex
var activePolicies = map[string]bool{}
var lockedPolicies = map[string]bool{}

func LogPolicyKey(panelURL string, machineID, nodeID int) string {
	if machineID > 0 {
		return fmt.Sprintf("%s|machine:%d", strings.TrimRight(strings.TrimSpace(panelURL), "/"), machineID)
	}
	return fmt.Sprintf("%s|node:%d", strings.TrimRight(strings.TrimSpace(panelURL), "/"), nodeID)
}

// 启动或重载时先静默，收到所有身份的明确策略后才允许输出。
// 已收到的停用策略在同一进程中永久保留，不受本地日志级别影响。
func ConfigureLogPolicies(keys []string) {
	policyMu.Lock()
	defer policyMu.Unlock()
	activePolicies = make(map[string]bool, len(keys))
	for _, key := range keys {
		activePolicies[key] = true
	}
	updateLogPolicyLocked()
}

func ApplyLogPolicy(key string, disabled bool) {
	policyMu.Lock()
	defer policyMu.Unlock()
	if _, active := activePolicies[key]; !active {
		return
	}
	if disabled {
		lockedPolicies[key] = true
	}
	activePolicies[key] = disabled || lockedPolicies[key]
	updateLogPolicyLocked()
}

func updateLogPolicyLocked() {
	disabled := false
	for _, blocked := range activePolicies {
		if blocked {
			disabled = true
			break
		}
	}
	// 等待已进入出口的写入结束，策略返回后不再留下迟到的输出。
	logMu.Lock()
	logsDisabled.Store(disabled)
	logMu.Unlock()
}

func LogsDisabled() bool { return logsDisabled.Load() }

// sing-box 的原始输出统一经过此出口，运行中停用也能立即阻止输出。
func WriteRuntime(message string) {
	WithRuntimeOutput(func() { fmt.Fprintln(logWriter, message) })
}

func WithRuntimeOutput(write func()) {
	logMu.RLock()
	defer logMu.RUnlock()
	if !LogsDisabled() {
		write()
	}
}
