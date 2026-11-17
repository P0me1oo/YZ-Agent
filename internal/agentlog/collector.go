// Package agentlog 为运行日志提供限量、独立上报和固定批次重试。
package agentlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const capacity = 4096

type collector struct {
	mu              sync.Mutex
	saveMu          sync.Mutex
	queue           []nlog.Event
	pending         *panel.AgentLogBatch
	path            string
	enabled         bool
	dropped         uint64
	failures        nlog.Failures
	logger          *nlog.NodeLog
	scope           string
	key             *privacy.State
	acceptLevel     func(string) bool
	revision        uint64
	savedRevision   uint64
	savedProtection string
	savedInfo       os.FileInfo
}

func (c *collector) add(event nlog.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) >= capacity {
		c.dropped++
		return
	}
	c.queue = append(c.queue, event)
	c.revision++
}
func (c *collector) next() *panel.AgentLogBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acceptLevel != nil {
		previous := len(c.queue)
		c.queue = slices.DeleteFunc(c.queue, func(event nlog.Event) bool {
			return !c.acceptLevel(event.Level)
		})
		if len(c.queue) != previous {
			c.revision++
		}
	}
	// 已固定的批次不删减记录，保留重试位置和面板防重编号。
	if c.pending != nil {
		return c.pending
	}
	if c.dropped > 0 && len(c.queue) < capacity {
		if c.acceptLevel == nil || c.acceptLevel("warn") {
			c.queue = append(c.queue, nlog.Event{
				Source: "agent", Level: "warn",
				Message:    fmt.Sprintf("日志队列已满，未保存 %d 条运行日志", c.dropped),
				OccurredAt: time.Now().Unix(),
			})
			c.revision++
		}
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
	c.revision++
	return c.pending
}

type saved struct {
	Pending *panel.AgentLogBatch `json:"pending,omitempty"`
	Queue   []nlog.Event         `json:"queue,omitempty"`
}

func (c *collector) save() error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	private, unlockPolicy := nlog.LockLogPolicy(c.scope)
	defer unlockPolicy()
	release := privacy.LockFileWrite(c.path)
	defer release()
	if c.path == "" {
		return nil
	}
	protection := privacy.ProtectionKey(c.path)
	if private {
		if c.key == nil {
			return errors.New("运行日志密钥尚未取得")
		}
		protection = privacy.StateKey(c.key)
	}
	info, err := os.Stat(c.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info != nil && (!info.Mode().IsRegular() || info.Size() > 24<<20) {
		return errors.New("日志缓存类型或大小无效")
	}
	c.mu.Lock()
	revision := c.revision
	if revision == c.savedRevision && protection == c.savedProtection && sameCacheFile(info, c.savedInfo) {
		c.mu.Unlock()
		return nil
	}
	state := saved{Pending: c.pending, Queue: append([]nlog.Event(nil), c.queue...)}
	c.mu.Unlock()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if private {
		// 本进程已校验并保存的原文件无需重复解密；外部变化仍必须验证归属。
		if !sameCacheFile(info, c.savedInfo) {
			old, readErr := os.ReadFile(c.path)
			if readErr != nil && !os.IsNotExist(readErr) {
				return readErr
			}
			if privacy.IsEncrypted(old) {
				plain, openErr := c.key.Open(old)
				clear(plain)
				if openErr != nil {
					return openErr
				}
			}
		}
		data, err = c.key.Seal(data, nil)
	} else {
		data, err = privacy.Protect(c.path, data)
	}
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
	var savedInfo os.FileInfo
	if err == nil {
		savedInfo, err = temp.Stat()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// 同目录临时文件在成功保存后才替换，失败保留原快照。
	if err := os.Rename(name, c.path); err != nil {
		return err
	}
	// 保存期间新加入的记录拥有新的修订号，下一轮仍会落盘。
	c.savedRevision, c.savedProtection, c.savedInfo = revision, protection, savedInfo
	return nil
}

func sameCacheFile(current, saved os.FileInfo) bool {
	if current == nil || saved == nil {
		return false
	}
	return os.SameFile(current, saved) && current.Size() == saved.Size() &&
		current.Mode() == saved.Mode() && current.ModTime().Equal(saved.ModTime())
}
func (c *collector) restore() error {
	if c.path == "" {
		return nil
	}
	if info, err := os.Stat(c.path); err == nil && info.Size() > 24<<20 {
		return errors.New("日志缓存大小无效")
	}
	data, err := os.ReadFile(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if privacy.IsEncrypted(data) {
		if c.key != nil {
			data, err = c.key.Open(data)
		} else {
			data, err = privacy.OpenFor(c.path, data)
		}
		if err != nil {
			return err
		}
	}
	if len(data) > 12*1024*1024 {
		return fmt.Errorf("日志缓存大小无效")
	}
	var state saved
	if err := json.Unmarshal(data, &state); err != nil {
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
	c.revision++
	return nil
}
func (c *collector) flush(ctx context.Context, client *panel.Client) {
	batch := c.next()
	if batch == nil {
		return
	}
	if err := c.save(); err != nil {
		c.failures.Local(c.logger, "Agent 日志缓存保存失败", err)
		return
	}
	c.failures.Local(c.logger, "Agent 日志缓存保存失败", nil)
	err := client.ReportAgentLog(ctx, batch)
	if !errors.Is(ctx.Err(), context.Canceled) {
		c.failures.Local(c.logger, "Agent 日志上报未确认", err)
	}
	if err != nil {
		return
	}
	c.mu.Lock()
	c.pending = nil
	c.revision++
	c.mu.Unlock()
	c.saveWithDiagnostic()
}

func (c *collector) saveWithDiagnostic() {
	c.failures.Local(c.logger, "Agent 日志缓存保存失败", c.save())
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
	prefix := fmt.Sprintf("agent-log:node:%d", panelCfg.NodeID)
	if panelCfg.MachineID > 0 {
		prefix = fmt.Sprintf("agent-log:machine:%d", panelCfg.MachineID)
	}
	c := &collector{
		logger: nlog.Scoped(cfg.LogScope(), panelCfg.NodeID, prefix),
		scope:  cfg.LogScope(), key: cfg.RuntimeLogState(),
		acceptLevel: client.AgentLogLevelAllowed,
	}
	if cfg.Kernel.ConfigDir != "" {
		c.path = filepath.Join(cfg.Kernel.ConfigDir, fmt.Sprintf("pending-agent-%x.json", sha256.Sum256([]byte(cfg.LogScope()))))
	}
	restoreErr := c.restore()
	unsubscribe := func() {}
	if restoreErr == nil {
		unsubscribe = nlog.SubscribeFiltered(cfg.LogScope(), func(event nlog.Event) bool {
			return client.AgentLogLevelAllowed(event.Level)
		}, c.add)
	}
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
				if run.Err() == nil {
					c.failures.Local(c.logger, "Agent 日志握手失败", err)
				}
				stop()
				c.enabled = err == nil && client.AgentLogEnabled()
				lastHandshake = time.Now()
			}
			if restoreErr != nil {
				// 恢复失败通常早于首次策略确认；握手后才能输出且不能覆盖原文件。
				c.failures.Local(c.logger, "Agent 日志缓存恢复失败，保留原文件", restoreErr)
			} else if c.enabled {
				flush, stop := context.WithTimeout(run, 10*time.Second)
				c.flush(flush, client)
				stop()
			}
			// 即使面板暂时不可用也保存队列，不能等网络恢复后才落盘。
			if restoreErr == nil {
				c.saveWithDiagnostic()
			}
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
		if restoreErr != nil {
			return
		}
		if c.enabled {
			flush, stop := context.WithTimeout(context.Background(), 3*time.Second)
			c.flush(flush, client)
			stop()
		}
		c.saveWithDiagnostic()
	}
}
