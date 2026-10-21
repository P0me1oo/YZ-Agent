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
var subscriptions = map[string]func(Event){}
var globalScope string

func SetGlobalScope(scope string) { eventMu.Lock(); globalScope = scope; eventMu.Unlock() }
func Subscribe(scope string, callback func(Event)) func() {
	eventMu.Lock()
	subscriptions[scope] = callback
	eventMu.Unlock()
	return func() { eventMu.Lock(); delete(subscriptions, scope); eventMu.Unlock() }
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [^-]*(?:PRIVATE KEY|CERTIFICATE)-----[\s\S]*`),
	regexp.MustCompile(`(?i)-----BEGIN [^-]*(?:PRIVATE KEY|CERTIFICATE)-----[\s\S]*?(?:-----END [^-]+-----|$)`),
	regexp.MustCompile(`(?i)(?:https?|wss?|ss|vmess|vless|trojan|hysteria2?|tuic)://[^\s"'<>]+`),
	regexp.MustCompile(`(?i)\bBearer\s+[^\s,"'}]+`),
	regexp.MustCompile(`(?i)\b[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9+/_=-]{32,}`),
	regexp.MustCompile(`(?i)["']?[a-z_]*(?:password|passwd|token|secret|cookie|authorization|private_key|api_key|uuid|auth_data)[a-z_]*["']?\s*[=:]\s*(?:"[^"\n]*"|'[^'\n]*'|[^\s,;}]+)`),
	regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`),
}

func Sanitize(message string) string {
	for _, pattern := range secretPatterns {
		message = pattern.ReplaceAllString(message, "[已隐藏]")
	}
	if i := strings.Index(message, "{\""); i >= 0 {
		message = message[:i] + "[配置已隐藏]"
	}
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
	eventMu.RLock()
	defer eventMu.RUnlock()
	if scope == "" {
		scope = globalScope
	}
	if callback := subscriptions[scope]; callback != nil {
		event.Message = Sanitize(event.Message)
		if event.OccurredAt == 0 {
			event.OccurredAt = time.Now().Unix()
		}
		callback(event)
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
