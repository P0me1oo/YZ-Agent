package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/gorilla/websocket"
)

// WSEvent types
const (
	WSEventSyncSnapshot  = "sync.snapshot"
	WSEventSyncConfig    = "sync.config"
	WSEventSyncUsers     = "sync.users"
	WSEventSyncUserDelta = "sync.user.delta"
	WSEventSyncDevices   = "sync.devices"   // panel → node: global device state
	WSEventSyncNodes     = "sync.nodes"     // panel → machine: node list changed
	WSEventReportDevices = "report.devices" // node → panel: report device snapshot
)

// WSEvent is a parsed data event delivered to the service layer.
type WSEvent struct {
	Type        string
	Version     StateVersion
	Config      *NodeConfig
	Users       []User
	DeltaAction string // "add" or "remove" (only for sync.user.delta)
	DeltaUsers  []User // users affected by the delta

	// Device sync fields
	DeviceUsers map[int][]string // userID -> IPs (for sync.devices)
	NodeID      int

	// Machine node discovery fields (for sync.nodes)
	Nodes []MachineNode
}

// WSStatusChange notifies the service when WS connectivity changes.
type WSStatusChange struct {
	Connected bool
}

// wsMessage is the JSON envelope for all WS messages.
type wsMessage struct {
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`
}

// Payload structures for data events.
type syncConfigPayload struct {
	Config    NodeConfig `json:"config"`
	Timestamp int64      `json:"timestamp"`
	NodeID    int        `json:"node_id"`
}

type syncUsersPayload struct {
	Users     []User `json:"users"`
	Timestamp int64  `json:"timestamp"`
	NodeID    int    `json:"node_id"`
}

type syncUserDeltaPayload struct {
	Action    string `json:"action"`
	Users     []User `json:"users"`
	Timestamp int64  `json:"timestamp"`
	NodeID    int    `json:"node_id"`
}

type deviceIPList []string

func (l *deviceIPList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*l = list
		return nil
	}

	// PHP encodes arrays with non-contiguous numeric keys as JSON objects.
	// Accept that legacy shape and restore the original numeric-key order.
	var keyed map[string]string
	if err := json.Unmarshal(data, &keyed); err != nil {
		return fmt.Errorf("decode device IP list: %w", err)
	}
	keys := make([]string, 0, len(keyed))
	for key := range keyed {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftErr := strconv.Atoi(keys[i])
		right, rightErr := strconv.Atoi(keys[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return keys[i] < keys[j]
	})
	list = make([]string, 0, len(keys))
	for _, key := range keys {
		list = append(list, keyed[key])
	}
	*l = list
	return nil
}

type deviceUsers map[int]deviceIPList

func (u *deviceUsers) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]")) {
		*u = deviceUsers{}
		return nil
	}
	var users map[int]deviceIPList
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("decode device users: %w", err)
	}
	*u = users
	return nil
}

// syncDevicesPayload carries global device state from panel.
type syncDevicesPayload struct {
	StateVersion
	Users     deviceUsers `json:"users"`
	Timestamp int64       `json:"timestamp"`
	NodeID    int         `json:"node_id"`
}

// syncNodesPayload carries the updated node list for a machine.
type syncNodesPayload struct {
	Nodes []MachineNode `json:"nodes"`
}

// WSClientConfig holds WebSocket client tuning options.
type WSClientConfig struct {
	Realtime         bool
	HeartbeatTimeout time.Duration
	StatusInterval   time.Duration
	HandshakeTimeout time.Duration
	BackoffInitial   time.Duration
	BackoffMax       time.Duration
	MachineID        int
}

// WSClient connects to the panel's Workerman WS server using native WebSocket.
// Authentication is done via query parameters (token + node_id) during the
// WebSocket handshake — no separate auth step needed.
type WSClient struct {
	wsURL    string // base WS URL, e.g. ws://panel.example.com:8076
	token    string
	nodeID   int
	onEvent  func(WSEvent)
	onStatus func(WSStatusChange)
	onPing   func() map[string]interface{}

	cfg WSClientConfig

	connected  atomic.Bool
	generation atomic.Uint64
	mu         sync.RWMutex
	pending    map[string]wsPending
	rpcSeq     atomic.Uint64

	// 写队列和确认等待表由 mu 保护，连接退出时一起移除。
	writeCh chan wsMessage
}

// NewWSClient creates a new WebSocket client.
// wsURL is the base WebSocket URL (e.g. "ws://panel.example.com:8076").
// token and nodeID are used for authentication via query parameters.
func NewWSClient(wsURL string, token string, nodeID int, cfg WSClientConfig, onEvent func(WSEvent), onStatus func(WSStatusChange), onPing func() map[string]interface{}) *WSClient {
	// Apply defaults
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 15 * time.Second
	}
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = time.Second
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = 60 * time.Second
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 130 * time.Second
		if cfg.Realtime {
			cfg.HeartbeatTimeout = 20 * time.Second
		}
	}
	return &WSClient{
		pending:  make(map[string]wsPending),
		wsURL:    wsURL,
		token:    token,
		nodeID:   nodeID,
		cfg:      cfg,
		onEvent:  onEvent,
		onStatus: onStatus,
		onPing:   onPing,
	}
}

func (w *WSClient) IsConnected() bool  { return w.connected.Load() }
func (w *WSClient) Generation() uint64 { return w.generation.Load() }

func (w *WSClient) notifyStatus(connected bool) {
	if w.onStatus != nil {
		w.onStatus(WSStatusChange{Connected: connected})
	}
}

// Run connects and reconnects until ctx is cancelled.
func (w *WSClient) Run(ctx context.Context) {
	backoff := w.cfg.BackoffInitial
	for {
		start := time.Now()
		err := w.connect(ctx)

		wasConnected := w.connected.Swap(false)
		if wasConnected {
			w.notifyStatus(false)
		}

		if err != nil {
			nlog.Core().Warn("ws disconnected", "error", err)
			if !wasConnected {
				w.notifyStatus(false)
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		// Reset backoff if the connection was up for a meaningful duration.
		if time.Since(start) > 2*time.Minute {
			backoff = w.cfg.BackoffInitial
		}

		// Apply exponential backoff with jitter to prevent thundering herd.
		jitter := time.Duration(rand.Int63n(int64(backoff / 5)))
		wait := backoff + jitter

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < w.cfg.BackoffMax {
			backoff = min(backoff*2, w.cfg.BackoffMax)
		}
	}
}

func (w *WSClient) connect(ctx context.Context) error {
	u, err := url.Parse(w.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	q := u.Query()
	q.Set("token", w.token)
	if w.cfg.MachineID > 0 {
		q.Set("machine_id", strconv.Itoa(w.cfg.MachineID))
	} else {
		q.Set("node_id", strconv.Itoa(w.nodeID))
	}
	if w.cfg.Realtime {
		q.Set("realtime", "1")
	}
	u.RawQuery = q.Encode()

	// URL 含认证信息，日志只记录连接状态。
	dialer := websocket.Dialer{HandshakeTimeout: w.cfg.HandshakeTimeout}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	conn.SetReadLimit(10 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(w.cfg.HandshakeTimeout))
	var first wsMessage
	if err := conn.ReadJSON(&first); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if first.Event != "auth.success" {
		return fmt.Errorf("websocket authentication was not accepted")
	}

	writeCh := make(chan wsMessage, 64)
	w.mu.Lock()
	w.writeCh = writeCh
	w.mu.Unlock()
	if !w.cfg.Realtime {
		w.generation.Add(1)
		w.connected.Store(true)
		w.notifyStatus(true)
	}
	_ = conn.SetReadDeadline(time.Now().Add(w.cfg.HeartbeatTimeout))
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var msg wsMessage
			if err := conn.ReadJSON(&msg); err != nil {
				errCh <- err
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(w.cfg.HeartbeatTimeout))
			w.handleMessage(msg)
			if msg.Event == "ping" {
				select {
				case writeCh <- wsMessage{Event: "pong"}:
				default:
					errCh <- fmt.Errorf("websocket heartbeat queue is full")
					return
				}
			}
		}
	}()
	defer func() {
		_ = conn.Close()
		<-done
		w.mu.Lock()
		w.writeCh = nil
		for id, waiter := range w.pending {
			select {
			case waiter.result <- wsReply{err: ErrWSUnavailable}:
			default:
			}
			delete(w.pending, id)
		}
		w.mu.Unlock()
	}()

	reportTicker := time.NewTicker(w.cfg.StatusInterval)
	defer reportTicker.Stop()
	write := func(msg wsMessage) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(msg)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			return fmt.Errorf("read: %w", err)
		case <-reportTicker.C:
			if !w.cfg.Realtime && w.onPing != nil {
				if stats := w.onPing(); stats != nil {
					data, err := json.Marshal(stats)
					if err != nil {
						return err
					}
					if err := write(wsMessage{Event: "node.status", Data: data}); err != nil {
						return err
					}
				}
			}
		case msg := <-writeCh:
			if err := write(msg); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
	}
}

func (w *WSClient) handleMessage(msg wsMessage) {
	switch msg.Event {
	case "ping":
		// Server ping — handled by the pong timer reset above
		nlog.Core().Debug("ws received ping")

	case "auth.success":
		nlog.Core().Debug("ws auth confirmed")

	case "sync.ready":
		if !w.connected.Load() {
			w.generation.Add(1)
			w.connected.Store(true)
			w.notifyStatus(true)
		}

	case "traffic.ack", "traffic.error", "state.ack", "state.error":
		w.receiveReceipt(msg)

	case WSEventSyncSnapshot:
		w.handleDataEvent(msg)

	case WSEventSyncConfig:
		w.handleDataEvent(msg)

	case WSEventSyncUsers:
		w.handleDataEvent(msg)

	case WSEventSyncUserDelta:
		w.handleDataEvent(msg)

	case WSEventSyncDevices:
		w.handleDataEvent(msg)

	case WSEventSyncNodes:
		w.handleDataEvent(msg)

	default:
		nlog.Core().Debug("ws unknown event", "event", msg.Event)
	}
}

func (w *WSClient) handleDataEvent(msg wsMessage) {
	var event WSEvent
	event.Type = msg.Event
	if err := json.Unmarshal(msg.Data, &event.Version); err != nil {
		return
	}

	// Helper to unmarshal and decode with weak Typing
	decodeData := func(data []byte, target interface{}) error {
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		return decodeWeakRaw(raw, target)
	}

	switch msg.Event {
	case WSEventSyncSnapshot:
		var p struct {
			NodeID int        `json:"node_id"`
			Config NodeConfig `json:"config"`
			Users  []User     `json:"users"`
		}
		if err := decodeData(msg.Data, &p); err != nil || p.Config.Protocol == "" {
			return
		}
		event.NodeID = p.NodeID
		event.Config = &p.Config
		event.Users = p.Users
		if event.Users == nil {
			event.Users = []User{}
		}

	case WSEventSyncConfig:
		nlog.Core().Debug("ws sync config event received")
		var p syncConfigPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode config payload", "error", err)
			return
		}
		if p.Config.Protocol == "" {
			nlog.Core().Warn("ws: config payload missing protocol")
			return
		}
		event.Config = &p.Config
		if p.NodeID > 0 {
			event.NodeID = p.NodeID
		} else if p.Config.NodeID > 0 {
			event.NodeID = p.Config.NodeID
		}

	case WSEventSyncUsers:
		nlog.Core().Debug("ws sync users event received")
		var p syncUsersPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode users payload", "error", err)
			return
		}
		if p.Users == nil {
			p.Users = []User{}
		}
		event.Users = p.Users
		event.NodeID = p.NodeID

	case WSEventSyncUserDelta:
		nlog.Core().Debug("ws sync user delta event received")
		var p syncUserDeltaPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode user delta payload", "error", err)
			return
		}
		if p.Action == "" {
			nlog.Core().Warn("ws: user delta payload missing action")
			return
		}
		if len(p.Users) == 0 {
			nlog.Core().Warn("ws: user delta payload has no users")
			return
		}
		event.DeltaAction = p.Action
		event.DeltaUsers = p.Users
		event.NodeID = p.NodeID

	case WSEventSyncDevices:
		nlog.Core().Debug("ws sync devices event received")
		var p syncDevicesPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode devices payload", "error", err)
			return
		}
		event.DeviceUsers = make(map[int][]string, len(p.Users))
		for userID, ips := range p.Users {
			event.DeviceUsers[userID] = []string(ips)
		}
		event.NodeID = p.NodeID

	case WSEventSyncNodes:
		nlog.Core().Info("ws sync nodes event received (machine node list changed)")
		var p syncNodesPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode nodes payload", "error", err)
			return
		}
		event.Nodes = p.Nodes
	}

	if w.onEvent != nil {
		w.onEvent(event)
	}
}

// SendDeviceReport sends local device snapshot to panel via WS.
func (w *WSClient) SendDeviceReport(devices map[int][]string) {
	w.SendDeviceReportForNode(0, devices)
}

// SendDeviceReportForNode sends a device report tagged with a specific node_id.
// nodeID == 0 omits the field (legacy single-node mode).
func (w *WSClient) SendDeviceReportForNode(nodeID int, devices map[int][]string) {
	if !w.connected.Load() {
		return
	}

	var payload interface{}
	if nodeID > 0 {
		payload = map[string]interface{}{"node_id": nodeID, "devices": devices}
	} else {
		payload = devices
	}

	d, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := wsMessage{
		Event:     WSEventReportDevices,
		Data:      d,
		Timestamp: time.Now().Unix(),
	}

	if !w.enqueue(msg) {
		nlog.Core().Warn("ws write channel unavailable, skipping device report")
	}
}

// SendNodeStatus sends a node.status event with the given node_id (machine mode).
func (w *WSClient) SendNodeStatus(nodeID int, stats map[string]interface{}) {
	if !w.connected.Load() || stats == nil {
		return
	}
	if nodeID > 0 {
		stats["node_id"] = nodeID
	}
	data, _ := json.Marshal(stats)
	msg := wsMessage{
		Event:     "node.status",
		Data:      data,
		Timestamp: time.Now().Unix(),
	}
	if !w.enqueue(msg) {
		nlog.Core().Warn("ws write channel unavailable, skipping node status")
	}
}

// SendRaw sends a raw message to the panel via WebSocket.
func (w *WSClient) SendRaw(event string, data json.RawMessage) {
	if !w.connected.Load() {
		return
	}

	msg := wsMessage{
		Event:     event,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}

	if !w.enqueue(msg) {
		nlog.Core().Warn("ws write channel unavailable, skipping raw message", "event", event)
	}
}
