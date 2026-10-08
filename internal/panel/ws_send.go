package panel

import "encoding/json"

const (
	wsQueueBytes     = 8 << 20
	wsControlReserve = 64 << 10
	wsPendingLimit   = 512
)

func prepareWSMessage(msg wsMessage) (wsMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return msg, err
	}
	msg.encoded = body
	return msg, nil
}

// enqueueLocked 同时限制消息条数和字节数，为少量控制请求保留容量。
func (w *WSClient) enqueueLocked(msg wsMessage) bool {
	if !w.connected.Load() || w.writeCh == nil {
		return false
	}
	queue, limit := w.writeCh, wsQueueBytes
	switch msg.Event {
	case "device.begin", "device.admit", "request.sync", "request.devices":
		if w.controlCh != nil && len(msg.encoded) <= wsControlReserve {
			queue, limit = w.controlCh, wsQueueBytes+wsControlReserve
		}
	}
	if len(msg.encoded) > limit-w.queuedBytes {
		return false
	}
	select {
	case queue <- msg:
		w.queuedBytes += len(msg.encoded)
		return true
	default:
		return false
	}
}

func (w *WSClient) enqueue(msg wsMessage) bool {
	msg, err := prepareWSMessage(msg)
	if err != nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enqueueLocked(msg)
}

// beginWrite 释放已经出队的字节，尚未发送就已取消的 RPC 不再滞留到恢复后执行。
func (w *WSClient) beginWrite(msg wsMessage) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queuedBytes -= len(msg.encoded)
	if msg.requestID != "" {
		_, pending := w.pending[msg.requestID]
		return pending
	}
	return true
}

func priorityMessage(pong <-chan struct{}, control <-chan wsMessage) (wsMessage, bool) {
	select {
	case <-pong:
		return wsMessage{Event: "pong"}, true
	default:
	}
	select {
	case msg := <-control:
		return msg, true
	default:
		return wsMessage{}, false
	}
}
