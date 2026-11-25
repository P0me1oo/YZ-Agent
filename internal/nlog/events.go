package nlog

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 订阅只用于内存队列；写日志的调用方不等待网络或磁盘。
type Event struct {
	Event      string `json:"event,omitempty"`
	NodeID     int    `json:"node_id,omitempty"`
	Source     string `json:"source"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	OccurredAt int64  `json:"occurred_at"`
}

var eventMu sync.RWMutex

type subscription struct {
	accept   func(Event) bool
	callback func(Event)
}

var subscriptions = map[string]subscription{}
var globalScope string

func SetGlobalScope(scope string) { eventMu.Lock(); globalScope = scope; eventMu.Unlock() }
func Subscribe(scope string, callback func(Event)) func() {
	return SubscribeFiltered(scope, nil, callback)
}

// 在文本整理和队列分配前筛选上传等级，本地日志出口仍按原等级记录。
func SubscribeFiltered(scope string, accept func(Event) bool, callback func(Event)) func() {
	eventMu.Lock()
	subscriptions[scope] = subscription{accept: accept, callback: callback}
	eventMu.Unlock()
	return func() { eventMu.Lock(); delete(subscriptions, scope); eventMu.Unlock() }
}

var logANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Sanitize 仅清理终端控制字符并限制长度，保留日志正文；隐私策略由日志出口独立执行。
func Sanitize(message string) string {
	message = logANSI.ReplaceAllString(message, "")
	message = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' || r == 127 {
			return -1
		}
		return r
	}, message)
	if len(message) > 1600 {
		message = message[:1600]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return strings.TrimSpace(message)
}
func Publish(scope string, event Event) {
	logMu.RLock()
	defer logMu.RUnlock()
	scope = effectiveScope(scope)
	if !logsReadyLocked(scope) {
		return
	}
	eventMu.RLock()
	defer eventMu.RUnlock()
	if subscriber := subscriptions[scope]; subscriber.callback != nil {
		if subscriber.accept != nil && !subscriber.accept(event) {
			return
		}
		event.Message = Sanitize(event.Message)
		if event.OccurredAt == 0 {
			event.OccurredAt = time.Now().Unix()
		}
		subscriber.callback(event)
	}
}
func Scoped(scope string, node int, prefix string) *NodeLog {
	return &NodeLog{prefix: prefix, scope: scope, nodeID: node}
}
func (nl *NodeLog) emit(level slog.Level, msg string, args []any) {
	logMu.RLock()
	min := logMin
	logMu.RUnlock()
	if level < min {
		return
	}
	severity := "info"
	switch {
	case level >= slog.LevelError:
		severity = "error"
	case level >= slog.LevelWarn:
		severity = "warn"
	case level <= slog.LevelDebug:
		severity = "debug"
	}
	Publish(nl.scope, Event{NodeID: nl.nodeID, Source: "agent", Level: severity, Message: formatMsg(msg, args)})
}
