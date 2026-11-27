package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// Shared across every node on this Agent. No waiting goroutine or unbounded
// queue: a busy node retries on a later service tick, with at most 8 route dials.
var healthSlots = make(chan struct{}, 4)
var healthReportSlots = make(chan struct{}, 8)

func (s *Service) updateHealth(ctx context.Context) {
	cfg := s.lastConfig
	c := cfg.HealthCheck
	running := s.kernel.IsRunning()
	runtime := "stopped"
	if running {
		runtime = "running"
	}
	if s.runtimeError != nil {
		runtime = "failed"
	}
	key := computeKernelConfigHash(cfg) + "|" + s.appliedState.TLSHash
	if c != nil {
		key += "|" + c.Generation + "|" + c.URL + "|" + c.Request
	}
	changed := key != s.operations.probeConfig || runtime != s.operations.probeRuntime
	if changed || c == nil || !c.Enabled {
		if s.operations.probeCancel != nil {
			s.operations.probeCancel()
			s.operations.probeCancel = nil
		}
		s.operations.probe.Store(nil)
		s.operations.lastProbe = time.Time{}
		if runtime != s.operations.probeRuntime || key != s.operations.probeConfig {
			var token [16]byte
			rand.Read(token[:])
			s.operations.run = fmt.Sprintf("%x", token)
		}
		s.operations.probeConfig = key
		s.operations.probeRuntime = runtime
	}
	if c == nil || !c.Enabled || (!running && c.Generation == "") {
		return
	}
	// Retry reporting separately, without retaining unlimited old samples.
	if result := s.operations.probe.Load(); result != nil && c.Generation != "" && (result.Generation != c.Generation || result.Request != c.Request || result.Run != s.operations.run) {
		s.operations.probe.Store(nil)
	}
	if result := s.operations.probe.Load(); result != nil && c.Generation != "" {
		reported := s.operations.healthReported.Load()
		if (reported == nil || *reported != result.ID) && time.Since(s.operations.lastHealthReport) >= 5*time.Second {
			if sink, ok := s.sink.(controlplane.HealthReporter); ok && s.operations.healthReportBusy.CompareAndSwap(false, true) {
				s.operations.lastHealthReport = time.Now()
				select {
				case healthReportSlots <- struct{}{}:
				default:
					s.operations.healthReportBusy.Store(false)
					return
				}
				go func() {
					defer s.operations.healthReportBusy.Store(false)
					defer func() { <-healthReportSlots }()
					job, cancel := context.WithTimeout(ctx, 3*time.Second)
					defer cancel()
					if sink.ReportHealth(job, result) == nil {
						id := result.ID
						s.operations.healthReported.Store(&id)
					}
				}()
			}
		}
	}
	interval := time.Duration(max(30, min(3600, c.Interval))) * time.Second
	if time.Since(s.operations.lastProbe) < interval && c.Request == s.operations.probeRequest {
		return
	}
	if !s.operations.probing.CompareAndSwap(false, true) {
		return
	}
	select {
	case healthSlots <- struct{}{}:
	default:
		s.operations.probing.Store(false)
		return
	}
	s.operations.lastProbe = time.Now()
	s.operations.probeRequest = c.Request
	target, request, generation := c.URL, c.Request, c.Generation
	configHash := computeKernelConfigHash(cfg)
	if s.operations.probeAddresses == nil || s.operations.probeAddressKey != configHash+"|"+target {
		s.operations.probeAddresses = &healthcheck.Prober{}
		s.operations.probeAddressKey = configHash + "|" + target
	}
	prober := s.operations.probeAddresses
	listen, port := cfg.ListenIP, cfg.ServerPort
	if listen == "" {
		listen = "::"
	}
	tcp := healthcheck.IsTCP(cfg.Protocol, cfg.Network, cfg.TLS, cfg.TLSSettings)
	if cfg.Protocol == "mieru" && cfg.Transport == "UDP" {
		tcp = false
	}
	if cfg.IsRelayLanding() {
		if cfg.Relay.ListenPort > 0 {
			port = cfg.Relay.ListenPort
		}
		switch cfg.Relay.Protocol {
		case "wireguard":
			tcp = false
		case "shadowsocks":
			tcp = true
		case "vless":
			tcp = healthcheck.IsTCP("vless", cfg.Network, cfg.TLS, cfg.TLSSettings)
		}
	}
	routes := map[string]int{"": 0}
	// Existing configurations have at most 4 children in a round. Rotate larger
	// sets instead of starving high-numbered routes forever.
	if cfg.Relay != nil && cfg.Relay.IsEntry() {
		count := len(cfg.Relay.Children)
		for i := 0; i < min(count, 4); i++ {
			child := cfg.Relay.Children[(s.operations.probeNext+i)%count]
			routes[child.Tag] = child.NodeID
		}
		if count > 0 {
			s.operations.probeNext = (s.operations.probeNext + 4) % count
		}
	}
	run := s.operations.run
	clients, landing := c.Clients, cfg.IsRelayLanding()
	forwarding, forwardError, nodeID := c.Forwarding, c.ForwardingError, s.cfg.Panel.NodeID
	scrub := healthErrorScrubber(c.Clients, c.Forwarding, cfg.TLSSettings)
	runtimeError := "代理核心未运行"
	if s.runtimeError != nil {
		runtimeError = nlog.Sanitize(s.runtimeError.Error())
	}
	job, cancel := context.WithTimeout(ctx, 30*time.Second)
	s.operations.probeCancel = cancel
	go func() {
		defer s.operations.probing.Store(false)
		defer func() { <-healthSlots }()
		defer cancel()
		start := time.Now()
		result := &healthcheck.Result{ID: fmt.Sprintf("%d", start.UnixNano()), Run: run, Generation: generation, Request: request, ConfigHash: configHash, Checked: start.Unix(), Status: "ok", Target: target, Source: "节点本机", Steps: []healthcheck.Step{}, Routes: []healthcheck.Route{}}
		if !running {
			status := "failed"
			result.Status = "failed"
			if runtime != "failed" {
				status = "not_supported"
				result.Status = "partial"
			}
			result.Steps = append(result.Steps, healthcheck.Step{Name: "runtime", Status: status, Error: runtimeError})
		} else {
			before := time.Now()
			network := "udp"
			if tcp {
				network = "tcp"
			}
			listening, err := healthcheck.ListenerNetwork(job, network, net.JoinHostPort(listen, strconv.Itoa(port)))
			step := healthcheck.Step{Name: "listener", Status: "ok", MS: time.Since(before).Milliseconds()}
			if err != nil {
				step.Status = "not_supported"
				step.Error = err.Error()
				result.Status = "partial"
			} else if !listening {
				step.Status = "failed"
				step.Error = "当前进程没有对应的 " + network + " 监听"
				result.Status = "failed"
			}
			result.Steps = append(result.Steps, step)
			dialer, ok := s.kernel.(interface {
				ProbeDial(context.Context, string, string, string) (net.Conn, error)
			})
			if !ok {
				result.Status = "unsupported"
			} else {
				before = time.Now()
				dnsCtx, dnsCancel := context.WithTimeout(job, 5*time.Second)
				u, addresses, err := healthcheck.Target(dnsCtx, target)
				dnsCancel()
				step = healthcheck.Step{Name: "target", Status: "ok", MS: time.Since(before).Milliseconds()}
				if err != nil {
					step.Status = "failed"
					step.Error = nlog.Sanitize(err.Error())
					result.Status = "failed"
				}
				result.Steps = append(result.Steps, step)
				if err == nil {
					result.Routes = healthcheck.CheckRoutes(job, routes, func(ctx context.Context, tag string) (int64, error) {
						if raw := clients[tag]; len(raw) > 0 {
							dial, closeClient, err := healthClient(ctx, raw)
							if err != nil {
								return 0, healthcheck.IncompleteError{Reason: err}
							}
							defer closeClient()
							return prober.Request(ctx, tag, u, addresses, dial)
						}
						return prober.Request(ctx, tag, u, addresses, func(ctx context.Context, network, address string) (net.Conn, error) {
							return dialer.ProbeDial(ctx, tag, network, address)
						})
					})
					if !landing && generation != "" {
						status, message := "incomplete", "本机协议请求未完成，请结合出站原始错误判断"
						for _, route := range result.Routes {
							if route.Tag == "" && route.Status == "ok" {
								status, message = "ok", ""
							}
						}
						if len(clients[""]) == 0 {
							status = "not_supported"
							message = "未取得可验证本机协议的授权客户端；仅验证核心出站"
							if result.Status == "ok" {
								result.Status = "partial"
							}
						}
						result.Steps = append(result.Steps, healthcheck.Step{Name: "protocol", Status: status, Error: message})
					}
					if len(forwarding) > 0 || forwardError != "" {
						route := healthcheck.Route{Tag: "forwarding", NodeID: nodeID, Status: "incomplete", Error: forwardError}
						if len(forwarding) > 0 {
							dial, closeClient, e := healthClient(job, forwarding)
							if e != nil {
								route.Error = e.Error()
							} else {
								route.MS, e = prober.Request(job, "forwarding", u, addresses, dial)
								closeClient()
								route.Status = "ok"
								if e != nil {
									route.Status = "failed"
									route.Error = e.Error()
								}
							}
						}
						result.Routes = append(result.Routes, route)
					}
					for i := range result.Routes {
						route := &result.Routes[i]
						route.Error = nlog.Sanitize(route.Error)
						if len(route.Error) > 1900 {
							route.Error = route.Error[:1900]
						}
						if route.Tag == "" {
							if route.Status == "failed" {
								result.Status = "failed"
							} else if route.Status == "incomplete" && result.Status == "ok" {
								result.Status = "partial"
							}
						}
					}
				}
			}
		}
		result.Duration = time.Since(start).Milliseconds()
		if generation == "" {
			result.Run = ""
			result.Request = ""
			result.Duration = 0
		}
		limit := 1900
		if generation == "" {
			limit = 200
		}
		for i := range result.Steps {
			value := scrub(result.Steps[i].Error)
			if len(value) > limit {
				value = strings.ToValidUTF8(value[:limit], "")
			}
			result.Steps[i].Error = value
		}
		for i := range result.Routes {
			value := scrub(result.Routes[i].Error)
			if len(value) > limit {
				value = strings.ToValidUTF8(value[:limit], "")
			}
			result.Routes[i].Error = value
		}
		// Reload/stop cancellation cannot publish a result for the replacement core.
		if ctx.Err() == nil && job.Err() != context.Canceled {
			s.operations.probe.Store(result)
		}
	}()
}

func healthClient(ctx context.Context, raw map[string]any) (healthcheck.DialFunc, func(), error) {
	if raw["engine"] == "xray" {
		return xray.DiagnosticClient(ctx, raw["outbound"])
	}
	return healthcheck.ForwardClient(ctx, raw)
}

func healthErrorScrubber(clients map[string]map[string]any, forward, tls map[string]any) func(string) string {
	secrets := []string{}
	var collect func(any)
	collect = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				switch strings.ToLower(key) {
				case "uuid", "id", "password", "private_key", "privatekey", "secretkey", "key", "pass":
					if text, ok := item.(string); ok && len(text) >= 4 {
						secrets = append(secrets, text)
					}
				}
				collect(item)
			}
		case []any:
			for _, item := range v {
				collect(item)
			}
		}
	}
	for _, client := range clients {
		collect(client)
	}
	collect(forward)
	collect(tls)
	return func(text string) string {
		text = nlog.Sanitize(text)
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[凭据已隐藏]")
		}
		return text
	}
}
