// Package accesslog 只保存连接目标的主机名、端口和用户编号，不读取通信内容。
package accesslog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"golang.org/x/net/idna"
)

const MaxBatch = 256
const MaxRecords = 32768
const Retention = 7 * 24 * time.Hour

type Record struct {
	NodeID      int    `json:"node_id,omitempty"`
	UserID      int    `json:"user_id"`
	ConnectedAt int64  `json:"connected_at"`
	Destination string `json:"destination"`
	Port        uint16 `json:"port"`
	RuleID      int    `json:"rule_id,omitempty"`
	Network     string `json:"network,omitempty"`
}

type Batch struct {
	ID         string   `json:"batch_id"`
	Records    []Record `json:"records"`
	Generation string   `json:"-"`
	sealed     bool
}

type savedState struct {
	Version    int      `json:"version"`
	Pending    *Batch   `json:"pending,omitempty"`
	Queue      []*Batch `json:"queue,omitempty"`
	Generation string   `json:"generation,omitempty"`
}

type Recorder struct {
	// 创建后不再改变；中转出站可为单条记录指定实际落地。
	NodeID          int
	enabled         atomic.Bool
	dropped         atomic.Uint64
	mu              sync.Mutex
	queue           []*Batch
	pending         *Batch
	count           int
	policy          atomic.Pointer[func() (bool, uint64)]
	discardRevision uint64
	generation      atomic.Pointer[func() string]
	generationValue string
}

// 策略只读取内存状态；连接回调不发请求。停用编号用于识别两次刷盘之间的关闭再开启。
func (r *Recorder) SetPolicy(policy func() (bool, uint64)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 新记录器以当前连接的停用编号为基线，不能把节点重建当成再次停用。
	if r.pending == nil && len(r.queue) == 0 {
		_, r.discardRevision = policy()
	}
	r.policy.Store(&policy)
}

// 面板持久保存的代次随缓存核对，进程重启也能识别停用前的记录。
func (r *Recorder) SetGeneration(generation func() string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil && len(r.queue) == 0 {
		r.generationValue = generation()
	}
	r.generation.Store(&generation)
}

func (r *Recorder) Enabled() bool {
	if r == nil || !r.enabled.Load() {
		return false
	}
	if policy := r.policy.Load(); policy != nil {
		enabled, _ := (*policy)()
		return enabled
	}
	return true
}

func (r *Recorder) syncPolicyLocked() bool {
	if generation := r.generation.Load(); generation != nil {
		value := (*generation)()
		if value != r.generationValue {
			r.clearLocked()
			r.generationValue = value
		}
	}
	if policy := r.policy.Load(); policy != nil {
		enabled, revision := (*policy)()
		if !enabled || revision != r.discardRevision {
			r.clearLocked()
		}
		r.discardRevision = revision
		return enabled
	}
	return true
}

func (r *Recorder) Enable(enabled bool) {
	if r != nil {
		r.enabled.Store(enabled)
	}
}
func (r *Recorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Swap(0)
}

var hostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
var numericAddress = regexp.MustCompile(`^[0-9.]+$`)
var batchID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func Host(value string) string {
	if ip, err := netip.ParseAddr(value); err == nil && ip.Zone() == "" {
		return ip.Unmap().String()
	}
	// 不从 URL 提取主机，避免把路径、查询参数或凭据带入记录。
	value, err := idna.Lookup.ToASCII(strings.TrimSuffix(value, "."))
	value = strings.ToLower(value)
	if err != nil || len(value) > 253 || !hostname.MatchString(value) || numericAddress.MatchString(value) {
		return ""
	}
	return value
}

// Add 的调用方只提供核心已经取得的目标信息；不做 DNS 反查或网络请求。
func (r *Recorder) Add(user int, domain, address string, port uint16) {
	r.AddForNode(0, user, domain, address, port)
}

// 节点 0 表示本节点直出，已入队的节点归属不随重载或重试改变。
func (r *Recorder) AddForNode(node, user int, domain, address string, port uint16) {
	if user <= 0 {
		return
	}
	r.add(node, user, domain, address, port, 0, "")
}

func (r *Recorder) add(node, user int, domain, address string, port uint16, rule int, network string) {
	if !r.Enabled() || node < 0 || port == 0 {
		return
	}
	if node == 0 {
		node = r.NodeID
	}
	host := Host(domain)
	if host == "" {
		host = Host(address)
	}
	if host == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled.Load() || !r.syncPolicyLocked() {
		return
	}
	if r.count >= MaxRecords {
		r.dropped.Add(1)
		return
	}
	var batch *Batch
	if len(r.queue) > 0 {
		batch = r.queue[len(r.queue)-1]
	}
	if batch == nil || batch.sealed || len(batch.Records) >= MaxBatch {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			r.dropped.Add(1)
			return
		}
		batch = &Batch{ID: hex.EncodeToString(id[:]), Generation: r.generationValue}
		r.queue = append(r.queue, batch)
	}
	batch.Records = append(batch.Records, Record{NodeID: node, UserID: user, ConnectedAt: time.Now().Unix(), Destination: host, Port: port, RuleID: rule, Network: network})
	r.count++
}

// 明确停用访问记录后不再补报旧队列；持久化入口会同步移除空缓存。
func (r *Recorder) Discard() {
	r.enabled.Store(false)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
}

func (r *Recorder) clearLocked() {
	r.queue = nil
	r.pending = nil
	r.count = 0
	r.dropped.Store(0)
}

// Next 返回固定批次；确认前重复调用得到同一编号和内容，新连接进入另一队列。
func (r *Recorder) Next() (*Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.syncPolicyLocked() {
		return nil, nil
	}
	cutoff := time.Now().Add(-Retention).Unix()
	if r.pending != nil {
		fresh := false
		for _, record := range r.pending.Records {
			if record.ConnectedAt >= cutoff {
				fresh = true
				break
			}
		}
		if fresh {
			return r.pending, nil
		}
		r.count -= len(r.pending.Records)
		r.pending = nil
	}
	valid := r.queue[:0]
	for _, batch := range r.queue {
		fresh := false
		for _, record := range batch.Records {
			if record.ConnectedAt >= cutoff {
				fresh = true
				break
			}
		}
		if fresh {
			valid = append(valid, batch)
		} else {
			r.count -= len(batch.Records)
		}
	}
	r.queue = valid
	if len(r.queue) == 0 {
		return nil, nil
	}
	r.pending = r.queue[0]
	r.pending.sealed = true
	r.queue = r.queue[1:]
	return r.pending, nil
}

func (r *Recorder) Ack(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil && r.pending.ID == id {
		r.count -= len(r.pending.Records)
		r.pending = nil
	}
}

// Save 只由上报协程调用。连接回调不做磁盘 I/O；正常退出会再保存一次。
func (r *Recorder) Save(path string) error {
	_, err := r.Checkpoint(path)
	return err
}

// Checkpoint 一次保存所有已封口的批次，返回这次可安全发送的批次数。
// 新连接创建新批次；中途退出时可以重发整份快照，面板按固定编号去重。
func (r *Recorder) Checkpoint(path string) (int, error) {
	r.mu.Lock()
	r.syncPolicyLocked()
	for _, batch := range r.queue {
		batch.sealed = true
	}
	state := savedState{
		Version:    1,
		Pending:    r.pending,
		Queue:      append([]*Batch(nil), r.queue...),
		Generation: r.generationValue,
	}
	r.mu.Unlock()
	count := len(state.Queue)
	if state.Pending != nil {
		count++
	}
	return count, writeState(path, state)
}

func writeState(path string, state savedState) error {
	release := privacy.LockFileWrite(path)
	defer release()
	if path == "" {
		return nil
	}
	if state.Pending == nil && len(state.Queue) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data, err = privacy.Protect(path, data)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".access-log-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (r *Recorder) Restore(path string) error {
	if path == "" {
		return nil
	}
	r.mu.Lock()
	revision := r.discardRevision
	r.mu.Unlock()
	data, err := privacy.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("访问记录缓存过大")
	}
	var state savedState
	if err = json.Unmarshal(data, &state); err != nil {
		return errors.New("访问记录缓存格式无效")
	}
	if state.Generation != "" && !batchID.MatchString(state.Generation) {
		return errors.New("访问记录缓存停用代次无效")
	}
	count := 0
	batches := append([]*Batch(nil), state.Queue...)
	if state.Pending != nil {
		batches = append(batches, state.Pending)
	}
	if state.Version != 1 || len(batches) > MaxRecords {
		return errors.New("访问记录缓存版本或数量无效")
	}
	validate := func(records []Record) error {
		for _, record := range records {
			block := record.RuleID > 0 && (record.Network == "tcp" || record.Network == "udp")
			if record.NodeID < 0 || record.UserID < 0 || (!block && (record.UserID == 0 || record.RuleID != 0 || record.Network != "")) || record.Port == 0 || record.ConnectedAt <= 0 || record.ConnectedAt > time.Now().Add(5*time.Minute).Unix() || Host(record.Destination) != record.Destination {
				return fmt.Errorf("访问记录缓存包含无效记录")
			}
		}
		return nil
	}
	seen := make(map[string]bool)
	for _, batch := range batches {
		if batch == nil || !batchID.MatchString(batch.ID) || seen[batch.ID] || len(batch.Records) == 0 || len(batch.Records) > MaxBatch {
			return errors.New("访问记录缓存批次无效")
		}
		seen[batch.ID] = true
		count += len(batch.Records)
		batch.sealed = true
		if err = validate(batch.Records); err != nil {
			return err
		}
	}
	if count > MaxRecords {
		return errors.New("访问记录缓存数量无效")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.syncPolicyLocked() || revision != r.discardRevision {
		return nil
	}
	if r.generation.Load() != nil && state.Generation != r.generationValue {
		return nil
	}
	for _, batch := range batches {
		batch.Generation = state.Generation
	}
	r.generationValue = state.Generation
	r.queue, r.pending = state.Queue, state.Pending
	r.count = count
	return nil
}
