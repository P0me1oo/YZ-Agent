package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

var ErrWSUnavailable = errors.New("websocket is not ready")

// StateVersion 标识同一运行或配置基线内的递增快照，不依赖节点时钟。
type StateVersion struct {
	Epoch    string `json:"epoch"`
	Sequence uint64 `json:"sequence"`
}

type RPCReceipt struct {
	StateVersion
	RequestID string           `json:"request_id"`
	ReportID  string           `json:"report_id"`
	NodeID    int              `json:"node_id"`
	Accepted  bool             `json:"accepted"`
	Code      int              `json:"code"`
	Telemetry *TelemetryDemand `json:"telemetry,omitempty"`
}

type RemoteError struct{ Code int }

func (e *RemoteError) Error() string { return fmt.Sprintf("panel rejected request (%d)", e.Code) }

type wsReply struct {
	receipt RPCReceipt
	err     error
}

type wsPending struct {
	nodeID int
	event  string
	result chan wsReply
}

// Request 只等待自己的确认，断线时立即失败。通道切换由调用方保留原业务批次执行。
func (w *WSClient) Request(ctx context.Context, event string, nodeID int, data map[string]interface{}) (RPCReceipt, error) {
	requestID := strconv.FormatUint(w.rpcSeq.Add(1), 10)
	payload := make(map[string]interface{}, len(data)+2)
	for key, value := range data {
		payload[key] = value
	}
	payload["request_id"] = requestID
	payload["node_id"] = nodeID
	body, err := json.Marshal(payload)
	if err != nil {
		return RPCReceipt{}, err
	}
	waiter := wsPending{nodeID: nodeID, event: event, result: make(chan wsReply, 1)}
	w.mu.Lock()
	if !w.connected.Load() || w.writeCh == nil {
		w.mu.Unlock()
		return RPCReceipt{}, ErrWSUnavailable
	}
	if w.pending == nil {
		w.pending = make(map[string]wsPending)
	}
	w.pending[requestID] = waiter
	select {
	case w.writeCh <- wsMessage{Event: event, Data: body}:
		w.mu.Unlock()
	default:
		delete(w.pending, requestID)
		w.mu.Unlock()
		return RPCReceipt{}, fmt.Errorf("websocket write queue is full")
	}
	defer func() {
		w.mu.Lock()
		delete(w.pending, requestID)
		w.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return RPCReceipt{}, ctx.Err()
	case result := <-waiter.result:
		return result.receipt, result.err
	}
}

func (w *WSClient) receiveReceipt(msg wsMessage) {
	var receipt RPCReceipt
	if json.Unmarshal(msg.Data, &receipt) != nil || receipt.RequestID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	waiter, ok := w.pending[receipt.RequestID]
	if !ok || waiter.nodeID != receipt.NodeID {
		return
	}
	valid := (waiter.event == "report.traffic" && (msg.Event == "traffic.ack" || msg.Event == "traffic.error")) ||
		((waiter.event == "runtime.state" || waiter.event == "machine.state") && (msg.Event == "state.ack" || msg.Event == "state.error"))
	if !valid {
		return
	}
	result := wsReply{receipt: receipt}
	if receipt.Code >= 400 {
		result.err = &RemoteError{Code: receipt.Code}
	}
	select {
	case waiter.result <- result:
	default:
	}
	delete(w.pending, receipt.RequestID)
}

func (w *WSClient) enqueue(msg wsMessage) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.connected.Load() || w.writeCh == nil {
		return false
	}
	select {
	case w.writeCh <- msg:
		return true
	default:
		return false
	}
}
