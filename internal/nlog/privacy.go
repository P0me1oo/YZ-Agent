package nlog

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

var logsDisabled atomic.Bool
var policyMu sync.Mutex
var activePolicies = map[string]bool{}
var knownPolicies = map[string]bool{}

type logScope struct {
	policy string
	writer *privacy.LogWriter
}

var logScopes = map[string]logScope{}
var privateWriters = map[string]*privacy.LogWriter{}
var privateOrder []string

func LogPolicyKey(panelURL string, machineID, nodeID int) string {
	if machineID > 0 {
		return fmt.Sprintf("%s|machine:%d", strings.TrimRight(strings.TrimSpace(panelURL), "/"), machineID)
	}
	return fmt.Sprintf("%s|node:%d", strings.TrimRight(strings.TrimSpace(panelURL), "/"), nodeID)
}

// 启动或重载时先静默，收到所有身份的明确策略后才允许输出。
// 隐私模式切换到加密出口，本地日志级别不能解除保护。
func ConfigureLogPolicies(keys []string) {
	policyMu.Lock()
	defer policyMu.Unlock()
	logMu.Lock()
	defer logMu.Unlock()
	activePolicies = make(map[string]bool, len(keys))
	knownPolicies = map[string]bool{}
	logScopes = map[string]logScope{}
	privateWriters = map[string]*privacy.LogWriter{}
	privateOrder = nil
	for _, key := range keys {
		activePolicies[key] = true
	}
	updateLogPolicyLocked()
}

func ApplyLogPolicy(key string, disabled bool) {
	policyMu.Lock()
	defer policyMu.Unlock()
	logMu.Lock()
	defer logMu.Unlock()
	if _, active := activePolicies[key]; !active {
		return
	}
	activePolicies[key] = disabled
	knownPolicies[key] = true
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
	logsDisabled.Store(disabled)
}

// LogsDisabled 表示共享明文出口被阻止；已登记的隐私身份仍可加密记录。
func LogsDisabled() bool { return logsDisabled.Load() }

// 已领取的机器密钥只登记在内存；普通模式也保留它，以便即时切换和恢复旧队列。
func RegisterLogScope(scope, policy, path string, state *privacy.State) {
	logMu.Lock()
	defer logMu.Unlock()
	item := logScope{policy: policy}
	if state != nil && path != "" {
		path = filepath.Clean(path)
		key := path + "|" + privacy.StateKey(state)
		item.writer = privateWriters[key]
		if item.writer == nil {
			item.writer = privacy.NewLogWriter(path, state)
			privateWriters[key] = item.writer
		}
	}
	if _, exists := logScopes[scope]; !exists {
		privateOrder = append(privateOrder, scope)
	}
	logScopes[scope] = item
}

func effectiveScope(scope string) string {
	if scope != "" {
		return scope
	}
	eventMu.RLock()
	defer eventMu.RUnlock()
	return globalScope
}

func PolicyReady(key string) bool {
	logMu.RLock()
	defer logMu.RUnlock()
	if len(activePolicies) == 0 {
		return true
	}
	if !knownPolicies[key] {
		return false
	}
	if !activePolicies[key] {
		return true
	}
	for _, item := range logScopes {
		if item.policy == key && item.writer != nil {
			return true
		}
	}
	return false
}

func LogsReady(scope string) bool {
	logMu.RLock()
	defer logMu.RUnlock()
	return logsReadyLocked(effectiveScope(scope))
}

func logsReadyLocked(scope string) bool {
	if item, ok := logScopes[scope]; ok {
		return knownPolicies[item.policy] && (!activePolicies[item.policy] || item.writer != nil)
	}
	return !LogsDisabled() || scope == "" && privateWriterLocked("") != nil
}

// 缓存写入持有此锁直到落盘，避免策略已切换后出现迟到的明文副本。
func LockLogPolicy(scope string) (bool, func()) {
	logMu.RLock()
	item, ok := logScopes[effectiveScope(scope)]
	return ok && knownPolicies[item.policy] && activePolicies[item.policy], logMu.RUnlock
}

// 调用方持有出口锁。共享核心日志没有可靠归属时，只写首个隐私身份的本地加密日志。
func privateWriterLocked(scope string) *privacy.LogWriter {
	if item, ok := logScopes[scope]; ok {
		if knownPolicies[item.policy] && activePolicies[item.policy] {
			return item.writer
		}
		return nil
	}
	if scope != "" {
		return nil
	}
	for _, name := range privateOrder {
		item := logScopes[name]
		if !knownPolicies[item.policy] {
			return nil
		}
		if activePolicies[item.policy] {
			return item.writer
		}
	}
	return nil
}

// sing-box 的原始输出统一经过此出口，运行中停用也能立即阻止输出。
func WriteRuntime(message string) {
	WriteRuntimeScoped("", message)
}

func WriteRuntimeScoped(scope, message string) {
	logMu.RLock()
	defer logMu.RUnlock()
	scope = effectiveScope(scope)
	if out := privateWriterLocked(scope); out != nil {
		_, _ = fmt.Fprintln(out, message)
		return
	}
	if logsReadyLocked(scope) {
		_, err := fmt.Fprintln(logWriter, message)
		reportWriterFailure(err)
	}
}

func WithRuntimeOutput(write func()) {
	logMu.RLock()
	defer logMu.RUnlock()
	if !LogsDisabled() {
		write()
	}
}
