package panel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWSQueueBoundsBytesAndPreservesControlAndHeartbeat(t *testing.T) {
	ws := NewWSClient("", "", 1, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 64)
	ws.controlCh = make(chan wsMessage, 16)
	ws.connected.Store(true)
	large := wsMessage{Event: "runtime.state", Data: json.RawMessage(`"` + strings.Repeat("a", wsQueueBytes-200) + `"`)}
	if !ws.enqueue(large) {
		t.Fatal("容量内消息被拒绝")
	}
	if ws.enqueue(wsMessage{Event: "runtime.state", Data: json.RawMessage(`"` + strings.Repeat("b", 4096) + `"`)}) {
		t.Fatal("字节容量已满却仍接受普通消息")
	}
	if !ws.enqueue(wsMessage{Event: "request.sync", Data: json.RawMessage(`{}`)}) {
		t.Fatal("普通消息占满后控制请求应有保留容量")
	}
	pong := make(chan struct{}, 1)
	pong <- struct{}{}
	msg, ok := priorityMessage(pong, ws.controlCh)
	if !ok || msg.Event != "pong" {
		t.Fatal("心跳没有优先发送")
	}
	msg, ok = priorityMessage(pong, ws.controlCh)
	if !ok || msg.Event != "request.sync" || !ws.beginWrite(msg) {
		t.Fatal("控制请求没有优先发送")
	}
	if len(ws.writeCh) != 1 {
		t.Fatal("优先发送不能丢弃普通消息")
	}
	msg = <-ws.writeCh
	if !ws.beginWrite(msg) || string(msg.Data) != string(large.Data) || ws.queuedBytes != 0 {
		t.Fatal("普通消息或出队字节统计不完整")
	}
}

func TestCanceledQueuedRPCDoesNotExecuteAfterRecovery(t *testing.T) {
	ws := NewWSClient("", "", 1, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 1)
	ws.connected.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ws.Request(ctx, "device.sync", 1, map[string]interface{}{}); done <- err }()
	msg := <-ws.writeCh
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal("取消没有结束等待")
	}
	if ws.beginWrite(msg) || ws.queuedBytes != 0 || len(ws.pending) != 0 {
		t.Fatal("过期请求仍被发送或遗留等待记录")
	}
}

func TestWSRequestOverloadReturnsFailureWithoutLeakingWaiters(t *testing.T) {
	ws := NewWSClient("", "", 1, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 1)
	ws.connected.Store(true)
	if !ws.enqueue(wsMessage{Event: "runtime.state"}) {
		t.Fatal("初始化队列失败")
	}
	if _, err := ws.Request(t.Context(), "report.traffic", 1, map[string]interface{}{}); err == nil || len(ws.pending) != 0 {
		t.Fatal("队列满必须明确失败，不能伪造计费确认或遗留等待记录")
	}
	ws.beginWrite(<-ws.writeCh)
	for i := range wsPendingLimit {
		ws.pending[strings.Repeat("p", i+1)] = wsPending{}
	}
	if _, err := ws.Request(t.Context(), "device.sync", 1, map[string]interface{}{}); err == nil || len(ws.pending) != wsPendingLimit || len(ws.writeCh) != 0 {
		t.Fatal("等待确认的请求也必须有数量上限")
	}
}
