// Package agentlog 为运行日志提供限量、独立上报和固定批次重试。
package agentlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const capacity = 4096

type collector struct {
	mu      sync.Mutex
	queue   []nlog.Event
	pending *panel.AgentLogBatch
	path    string
	enabled bool
	dropped uint64
}

func (c *collector) add(event nlog.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) >= capacity {
		c.dropped++
		return
	}
	c.queue = append(c.queue, event)
}
func (c *collector) next() *panel.AgentLogBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		return c.pending
	}
	if c.dropped > 0 && len(c.queue) < capacity {
		c.queue = append(c.queue, nlog.Event{Source: "agent", Level: "warn", Message: fmt.Sprintf("日志队列已满，未保存 %d 条运行日志", c.dropped), OccurredAt: time.Now().Unix()})
		c.dropped = 0
	}
	if len(c.queue) == 0 {
		return nil
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil
	}
	count := min(128, len(c.queue))
	c.pending = &panel.AgentLogBatch{ID: hex.EncodeToString(id), Records: append([]nlog.Event(nil), c.queue[:count]...)}
	c.queue = append([]nlog.Event(nil), c.queue[count:]...)
	return c.pending
}

type saved struct {
	Pending *panel.AgentLogBatch `json:"pending,omitempty"`
	Queue   []nlog.Event         `json:"queue,omitempty"`
}

func (c *collector) save() error {
	if c.path == "" {
		return nil
	}
	c.mu.Lock()
	state := saved{c.pending, append([]nlog.Event(nil), c.queue...)}
	c.mu.Unlock()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(c.path), ".agent-log-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// 同目录临时文件在成功保存后才替换，失败保留原快照。
	return os.Rename(name, c.path)
}
func (c *collector) restore() error {
	if c.path == "" {
		return nil
	}
	file, err := os.Open(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 12*1024*1024 {
		return fmt.Errorf("日志缓存大小无效")
	}
	var state saved
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		return err
	}
	valid := func(event nlog.Event) bool {
		validKind := event.Event == "" || event.Event == "xray.crash" && event.Source == "xray" && (event.Level == "error" || event.Level == "fatal")
		return validKind && event.NodeID >= 0 && event.OccurredAt > 0 && event.OccurredAt <= time.Now().Add(5*time.Minute).Unix() &&
			(event.Source == "agent" || event.Source == "xray" || event.Source == "singbox") &&
			(event.Level == "debug" || event.Level == "info" || event.Level == "warn" || event.Level == "error" || event.Level == "fatal") &&
			len(event.Message) <= 1600
	}
	if len(state.Queue) > capacity || state.Pending != nil && (len(state.Pending.ID) != 32 || len(state.Pending.Records) > 128) {
		return fmt.Errorf("日志缓存格式无效")
	}
	if state.Pending != nil {
		if _, err := hex.DecodeString(state.Pending.ID); err != nil || len(state.Pending.Records) == 0 {
			return fmt.Errorf("日志缓存批次无效")
		}
		for _, event := range state.Pending.Records {
			if !valid(event) {
				return fmt.Errorf("日志缓存条目无效")
			}
		}
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	for _, event := range state.Queue {
		if !valid(event) {
			return fmt.Errorf("日志缓存条目无效")
		}
		if event.OccurredAt >= cutoff {
			c.queue = append(c.queue, event)
		}
	}
	// 不删减 pending 内的位置，过期项由面板确认并丢弃，保持幂等键一致。
	c.pending = state.Pending
	return nil
}
func (c *collector) flush(ctx context.Context, client *panel.Client) {
	if nlog.LogsDisabled() {
		return
	}
	batch := c.next()
	if batch == nil || c.save() != nil {
		return
	}
	if client.ReportAgentLog(ctx, batch) != nil {
		return
	}
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	_ = c.save()
}
func Start(ctx context.Context, cfg *config.Config) func() {
	if cfg.IsStandalone() {
		return func() {}
	}
	panelCfg := cfg.Panel
	if cfg.Machine != nil {
		panelCfg.MachineID = cfg.Machine.MachineID
		panelCfg.Token = cfg.Machine.Token
		panelCfg.NodeID = 0
	}
	client := panel.NewClient(panelCfg)
	c := &collector{}
	if cfg.Kernel.ConfigDir != "" {
		c.path = filepath.Join(cfg.Kernel.ConfigDir, fmt.Sprintf("pending-agent-%x.json", sha256.Sum256([]byte(cfg.LogScope()))))
	}
	if err := c.restore(); err != nil {
		nlog.Core().Warn("Agent 日志缓存恢复失败，保留原文件")
		return func() {}
	}
	unsubscribe := nlog.Subscribe(cfg.LogScope(), c.add)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		lastHandshake := time.Time{}
		for {
			if time.Since(lastHandshake) >= time.Minute {
				handshake, stop := context.WithTimeout(run, 10*time.Second)
				_, err := client.HandshakeContext(handshake)
				stop()
				c.enabled = err == nil && client.AgentLogEnabled()
				lastHandshake = time.Now()
			}
			if c.enabled {
				flush, stop := context.WithTimeout(run, 10*time.Second)
				c.flush(flush, client)
				stop()
			}
			// 即使面板暂时不可用也保存队列，不能等网络恢复后才落盘。
			_ = c.save()
			select {
			case <-run.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
		unsubscribe()
		if c.enabled {
			flush, stop := context.WithTimeout(context.Background(), 3*time.Second)
			c.flush(flush, client)
			stop()
		}
		_ = c.save()
	}
}
