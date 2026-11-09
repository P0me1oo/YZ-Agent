package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/autospeed"
	"github.com/P0me1oo/YZ-Agent/internal/cert"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type operationsState struct {
	run               string
	sequence          uint64
	loaded            bool
	requestLoadFailed bool
	snapshot          atomic.Pointer[operationsSnapshot]
	probe             atomic.Pointer[healthcheck.Result]
	probing           atomic.Bool
	renewBusy         atomic.Bool
	renewDone         atomic.Pointer[renewResult]
	renewID           string
	lastProbe         time.Time
	probeRequest      string
}
type renewResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type operationsSnapshot struct {
	Run            string                    `json:"run"`
	Sequence       uint64                    `json:"sequence"`
	ErrorID        string                    `json:"error_id,omitempty"`
	SampledAt      int64                     `json:"sampled_at"`
	Version        int                       `json:"version"`
	Runtime        string                    `json:"runtime"`
	Error          string                    `json:"error,omitempty"`
	ConfigHash     string                    `json:"config_hash"`
	Control        panel.StateVersion        `json:"control"`
	Health         *healthcheck.Result       `json:"health,omitempty"`
	Certificate    *cert.Status              `json:"certificate,omitempty"`
	Renewal        *renewResult              `json:"renewal,omitempty"`
	PendingBytes   int64                     `json:"pending_bytes"`
	AutoSpeed      map[int]autospeed.Penalty `json:"auto_speed"`
	AutoSpeedError string                    `json:"auto_speed_error,omitempty"`
}

func (s *Service) updateOperations(ctx context.Context) {
	if s.lastConfig == nil {
		return
	}
	if s.operations.run == "" {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			s.failures.Report(s.logger(), "运行检查标识生成失败", err)
			return
		}
		s.operations.run = fmt.Sprintf("%x", token)
	}
	cfg := s.lastConfig
	if !s.operations.loaded {
		s.operations.loaded = true
		if path := s.renewalPath(); path != "" {
			data, err := privacy.ReadFile(path)
			if err == nil {
				var previous renewResult
				if err := json.Unmarshal(data, &previous); err == nil {
					if previous.Status == "running" {
						previous.Status = "failed"
						previous.Error = "上次续期被中断，请重新提交"
					}
					s.operations.renewID = previous.ID
					s.operations.renewDone.Store(&previous)
				} else {
					s.failures.Report(s.logger(), "证书续期记录解析失败，保留原文件", err)
					s.operations.requestLoadFailed = true
				}
			} else if !os.IsNotExist(err) {
				s.failures.Report(s.logger(), "证书续期记录读取失败，保留原文件", err)
				s.operations.requestLoadFailed = true
			}
		}
	}
	if request := cfg.CertificateRenew; request != "" && request != s.operations.renewID && !s.operations.requestLoadFailed && s.operations.renewBusy.CompareAndSwap(false, true) {
		s.operations.renewID = request
		initial := &renewResult{ID: request, Status: "running"}
		if err := s.saveRenewal(initial); err != nil {
			s.failures.Report(s.logger(), "证书续期请求保存失败", err)
			initial.Status = "failed"
			initial.Error = "无法保存续期请求，未执行续期"
			s.operations.renewDone.Store(initial)
			s.operations.renewBusy.Store(false)
			return
		}
		s.operations.renewDone.Store(initial)
		go func() {
			defer s.operations.renewBusy.Store(false)
			job, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			result := &renewResult{ID: request, Status: "succeeded"}
			if err := s.cert.Renew(job); err != nil {
				s.failures.Report(s.logger(), "证书续期失败", err)
				result.Status = "failed"
				result.Error = "证书续期失败，请检查验证设置与节点日志"
			}
			s.failures.Report(s.logger(), "证书续期结果保存失败", s.saveRenewal(result))
			s.operations.renewDone.Store(result)
		}()
	}
	c := cfg.HealthCheck
	if c != nil && c.Enabled && s.kernel.IsRunning() && len(s.lastUsers) > 0 || c != nil && c.Enabled && s.kernel.IsRunning() && cfg.IsRelayLanding() {
		interval := time.Duration(max(30, min(3600, c.Interval))) * time.Second
		if (time.Since(s.operations.lastProbe) >= interval || c.Request != s.operations.probeRequest) && s.operations.probing.CompareAndSwap(false, true) {
			s.operations.lastProbe = time.Now()
			s.operations.probeRequest = c.Request
			target := c.URL
			port := cfg.ServerPort
			listen := cfg.ListenIP
			if listen == "" || listen == "0.0.0.0" {
				listen = "127.0.0.1"
			} else if listen == "::" {
				listen = "::1"
			}
			if cfg.IsRelayLanding() && cfg.Relay.ListenPort > 0 {
				port = cfg.Relay.ListenPort
			}
			tcp := healthcheck.IsTCP(cfg.Protocol, cfg.Network, cfg.TLS, cfg.TLSSettings)
			routes := map[string]int{"": 0}
			totalRoutes := 1
			if cfg.Relay != nil && cfg.Relay.IsEntry() {
				totalRoutes += len(cfg.Relay.Children)
			}
			if cfg.Relay != nil && cfg.Relay.IsEntry() {
				for _, child := range cfg.Relay.Children {
					if len(routes) >= 17 {
						break
					}
					routes[child.Tag] = child.NodeID
				}
			}
			generation := computeKernelConfigHash(cfg)
			go func() {
				defer s.operations.probing.Store(false)
				job, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				result := &healthcheck.Result{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Checked: time.Now().Unix(), Status: "ok", Source: "节点本机", ConfigHash: generation, Target: target, Steps: []healthcheck.Step{}}
				if tcp {
					start := time.Now()
					conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(job, "tcp", net.JoinHostPort(listen, strconv.Itoa(port)))
					step := healthcheck.Step{Name: "listener", Status: "ok", MS: time.Since(start).Milliseconds()}
					if err != nil {
						step.Status = "failed"
						step.Error = "本机监听连接失败"
						result.Status = "failed"
					} else {
						conn.Close()
					}
					result.Steps = append(result.Steps, step)
				} else {
					result.Steps = append(result.Steps, healthcheck.Step{Name: "listener", Status: "not_supported", Error: "UDP 入站不以 TCP 探测"})
				}
				dialer, ok := s.kernel.(interface {
					ProbeDial(context.Context, string, string, string) (net.Conn, error)
				})
				if !ok {
					result.Status = "unsupported"
				} else if u, address, err := healthcheck.Target(job, target); err != nil {
					result.Status = "failed"
					result.Steps = append(result.Steps, healthcheck.Step{Name: "target", Status: "failed", Error: err.Error()})
				} else {
					result.Routes = healthcheck.CheckRoutes(job, routes, func(ctx context.Context, tag string) (int64, error) {
						return healthcheck.Request(ctx, u, address, func(ctx context.Context, network, address string) (net.Conn, error) {
							return dialer.ProbeDial(ctx, tag, network, address)
						})
					})
					for _, route := range result.Routes {
						if route.Status == "failed" {
							result.Status = "failed"
						} else if route.Status == "incomplete" && result.Status == "ok" {
							result.Status = "partial"
						}
					}
				}
				if totalRoutes > len(routes) {
					result.Steps = append(result.Steps, healthcheck.Step{Name: "routes", Status: "not_supported", Error: fmt.Sprintf("本次检查 %d/%d 个出站", len(routes), totalRoutes)})
					if result.Status == "ok" {
						result.Status = "partial"
					}
				}
				// 结果带配置指纹，主循环丢弃重载前的迟到结果。
				s.operations.probe.Store(result)
			}()
		}
	}
	s.operations.sequence++
	snapshot := &operationsSnapshot{Run: s.operations.run, Sequence: s.operations.sequence, ErrorID: s.runtimeErrorID, Version: 1, SampledAt: time.Now().UnixMilli(), Runtime: "running", ConfigHash: computeKernelConfigHash(cfg), Control: s.controlVersion.Current(), Health: s.operations.probe.Load(), AutoSpeed: s.autoSpeed.manager.Snapshot(time.Now()), AutoSpeedError: s.autoSpeed.error, Renewal: s.operations.renewDone.Load()}
	if c == nil || !c.Enabled || snapshot.Health != nil && snapshot.Health.ConfigHash != snapshot.ConfigHash {
		snapshot.Health = nil
	}
	if !s.kernel.IsRunning() {
		snapshot.Runtime = "stopped"
		snapshot.Health = nil
		s.operations.probe.Store(nil)
		s.operations.lastProbe = time.Time{}
	}
	if s.runtimeError != nil {
		snapshot.Runtime = "failed"
		message := nlog.Sanitize(s.runtimeError.Error())
		for utf8.RuneCountInString(message) > 280 {
			_, size := utf8.DecodeLastRuneInString(message)
			message = message[:len(message)-size]
		}
		snapshot.Error = message
	}
	if s.cert != nil {
		value := s.cert.Status()
		value.Applied = s.kernel.IsRunning() && s.appliedState.TLSHash == s.currentTLSHash()
		snapshot.Certificate = &value
	}
	// 只统计入口用户流量，内部中转明细不重复累计。
	s.reportMu.Lock()
	pending, _, _ := s.tracker.PendingSnapshot()
	for _, v := range pending {
		snapshot.PendingBytes += v[0] + v[1]
	}
	if s.retryReport != nil {
		for _, v := range s.retryReport.payload.Traffic {
			snapshot.PendingBytes += v[0] + v[1]
		}
	}
	s.reportMu.Unlock()
	s.operations.snapshot.Store(snapshot)
}

func (s *Service) renewalPath() string {
	if s.pendingStore == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(s.pendingStore.path), "renewal-"+filepath.Base(s.pendingStore.path))
}
func (s *Service) saveRenewal(value *renewResult) error {
	path := s.renewalPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return privacy.WriteFile(path, data, 0600)
}
