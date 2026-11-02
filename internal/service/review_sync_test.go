package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestDiscardedRESTSnapshotClearsETagsAndReconciles(t *testing.T) {
	for _, eventType := range []controlplane.EventType{controlplane.EventSyncConfig, controlplane.EventSyncUsers} {
		t.Run(string(eventType), func(t *testing.T) {
			var version atomic.Int32
			version.Store(1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := version.Load()
				etag := fmt.Sprintf(`"%s-%d"`, r.URL.Path, v)
				if r.Header.Get("If-None-Match") == etag {
					w.WriteHeader(304)
					return
				}
				w.Header().Set("ETag", etag)
				if r.URL.Path == "/api/v1/server/UniProxy/config" {
					fmt.Fprintf(w, `{"protocol":"vless","server_port":%d}`, 10000+v)
				} else {
					fmt.Fprintf(w, `{"users":[{"id":%d,"uuid":"test-user-%d"}]}`, v, v)
				}
			}))
			defer server.Close()
			cp := controlplane.NewPanelControlPlane(config.PanelConfig{URL: server.URL, NodeID: 1}, config.WSConfig{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			baseline, err := cp.Poll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := newTestService(&fakeKernel{running: true})
			s.source, s.pullResults = cp, make(chan pullResult, 1)
			s.lastConfig, s.lastConfigHash = baseline.Config, computeConfigHash(baseline.Config)
			s.updateUserState(baseline.Users)
			version.Store(2)
			snapshot, err := cp.Poll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			generation := s.controlGeneration
			s.pullPending.Store(true)
			event := controlplane.Event{Type: eventType, Config: &model.NodeSpec{Protocol: "vless", ServerPort: 10003}, Users: []model.UserSpec{{ID: 3, UUID: "test-user-3"}}}
			s.handleWSEvent(ctx, event)
			s.handleWSEvent(ctx, event) // 重复推送不推进代次。
			s.applyPullResult(ctx, pullResult{fromPoll: true, generation: generation, config: snapshot.Config, users: snapshot.Users, configHash: computeConfigHash(snapshot.Config), userHash: computeUserHash(snapshot.Users)})
			select {
			case result := <-s.pullResults:
				s.applyPullResult(ctx, result)
			case <-time.After(time.Second):
				t.Fatal("丢弃后未重新对账")
			}
			if s.lastConfig.ServerPort != 10002 || len(s.lastUsers) != 1 || s.lastUsers[0].ID != 2 {
				t.Fatal("ETag 导致面板状态长期遗漏")
			}
			unchanged, err := cp.Poll(ctx)
			if err != nil || unchanged.Config != nil || unchanged.Users != nil {
				t.Fatal("接纳后没有恢复条件请求")
			}
		})
	}
}

func TestLegacyDomainChangeReconfiguresCertificateBeforeCoreReload(t *testing.T) {
	s := newTestService(&fakeKernel{})
	s.cfg.Cert = config.CertConfig{CertMode: "self", Domain: "a.example.test", CertDir: t.TempDir()}
	s.lastUsers = []model.UserSpec{{ID: 1}}
	for _, domain := range []string{"a.example.test", "b.example.test"} {
		node := &model.NodeSpec{Protocol: "vless", ServerPort: 10001, Domain: domain}
		if !s.applyConfigUpdate(t.Context(), node, computeConfigHash(node)) {
			t.Fatal(s.runtimeError)
		}
		material := s.tlsCert()
		pair, err := tls.X509KeyPair(material.CertPEM, material.KeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := leaf.VerifyHostname(domain); err != nil {
			t.Fatal("旧字段改域名仍使用旧证书", err)
		}
	}
}

type waitingPush struct{}

func (*waitingPush) Run(ctx context.Context)           { <-ctx.Done() }
func (*waitingPush) IsConnected() bool                 { return true }
func (*waitingPush) SendDeviceReport(map[int][]string) {}

type waitingConfigPlane struct {
	controlplane.ControlPlane
	polls atomic.Int32
}

func (*waitingConfigPlane) Initial(context.Context, func() map[string]interface{}, chan<- controlplane.Event, chan<- controlplane.StatusChange) (controlplane.Bootstrap, error) {
	return controlplane.Bootstrap{Push: &waitingPush{}}, nil
}
func (p *waitingConfigPlane) Poll(context.Context) (controlplane.Snapshot, error) {
	p.polls.Add(1)
	return controlplane.Snapshot{}, errors.New("初始化对账暂不可用")
}
func (*waitingConfigPlane) SupportsPolling() bool       { return true }
func (*waitingConfigPlane) SupportsDiscovery() bool     { return false }
func (*waitingConfigPlane) SupportsReporting() bool     { return false }
func (*waitingConfigPlane) SupportsDeviceReports() bool { return false }

func TestUsersBeforeConfigurationStayStartingAndRecover(t *testing.T) {
	for _, kind := range []controlplane.EventType{controlplane.EventSyncUsers, controlplane.EventSyncUserDelta} {
		t.Run(string(kind), func(t *testing.T) {
			s := newTestService(&fakeKernel{})
			s.cfg.Node = config.NodeConfig{TrackInterval: 60, DeviceReportInterval: 60}
			s.cfg.WS.DiscoveryInterval = 60
			plane := &waitingConfigPlane{}
			s.source, s.sink = plane, plane
			s.wsEvents, s.wsStatusCh, s.pullResults = make(chan controlplane.Event, 8), make(chan controlplane.StatusChange, 2), make(chan pullResult, 1)
			statuses := make(chan RuntimeStatus, 8)
			s.SetStatusHandler(func(status RuntimeStatus) { statuses <- status })
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx) }()
			defer func() { cancel(); <-done }()
			if <-statuses != RuntimeStarting {
				t.Fatal("初始状态错误")
			}
			users := []model.UserSpec{{ID: 1, UUID: "test-user"}}
			s.wsEvents <- controlplane.Event{Type: kind, Users: users, DeltaUsers: users, DeltaAction: "add"}
			deadline := time.After(time.Second)
			for plane.polls.Load() == 0 {
				select {
				case <-deadline:
					t.Fatal("用户先到未请求快照")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			select {
			case status := <-statuses:
				t.Fatalf("配置未到就提前报告 %s", status)
			default:
			}
			s.wsEvents <- controlplane.Event{Type: controlplane.EventSyncConfig, Config: &model.NodeSpec{Protocol: "vless", ServerPort: 10001}}
			select {
			case status := <-statuses:
				if status != RuntimeRunning {
					t.Fatalf("有效配置后状态=%s", status)
				}
			case <-time.After(time.Second):
				t.Fatal("未从暂存用户恢复")
			}
		})
	}
}

func TestAPIBackoffGrowsAndResetsIndependently(t *testing.T) {
	var pull, push apiBackoff
	for _, base := range []int{1, 2, 4, 8, 8} {
		pull.onFailure()
		count := 0
		for pull.shouldSkip() {
			count++
		}
		if count < base || count > base+base/4 {
			t.Fatalf("退避=%d，预期 %d 至 %d", count, base, base+base/4)
		}
		if push.shouldSkip() {
			t.Fatal("拉取退避影响上报")
		}
	}
	pull.onSuccess()
	pull.onFailure()
	if !pull.shouldSkip() || pull.shouldSkip() {
		t.Fatal("成功后未复位")
	}
}
