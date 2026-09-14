package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gofrs/uuid/v5"
)

func TestDeltaRemovalRestartsCorrectedRuntime(t *testing.T) {
	ctx := context.Background()
	k := &fakeKernel{startErr: errors.New("测试用户应用失败")}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 12345}
	s.updateUserState([]model.UserSpec{{ID: 1}, {ID: 2}})
	if s.startKernel(ctx, s.lastConfig, s.lastUsers) || k.startCalls != 1 {
		t.Fatal("故障注入未生效")
	}
	k.startErr = nil
	if s.applyUserDelta(ctx, "remove", []model.UserSpec{{ID: 99}}) || k.startCalls != 1 {
		t.Fatal("删除不存在的用户不应重试相同失败快照")
	}
	if !s.applyUserDelta(ctx, "remove", []model.UserSpec{{ID: 2}}) {
		t.Fatal("删除导致故障的用户后未恢复运行")
	}
	if !k.running || k.startCalls != 2 || s.runtimeError != nil || len(k.startUsers) != 1 || k.startUsers[0].ID != 1 {
		t.Fatalf("恢复时未应用剩余用户: running=%v starts=%d error=%v", k.running, k.startCalls, s.runtimeError)
	}
	s.applyUserDelta(ctx, "remove", []model.UserSpec{{ID: 2}})
	if k.startCalls != 2 {
		t.Fatal("重复删除触发了额外启动")
	}
}

func TestUserChangesCannotBypassRemoteCertificateFailure(t *testing.T) {
	for _, action := range []string{"full", "add", "remove", "remove_last", "uuid"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			k := &fakeKernel{running: true}
			s := newTestService(k)
			s.cfg.Cert.CertDir = t.TempDir()
			s.updateUserState([]model.UserSpec{{ID: 1}, {ID: 2}})
			pending := &model.NodeSpec{
				Protocol: "vless", ServerPort: 12345, TLS: 1,
				CertConfig: &config.CertConfig{
					CertMode: "file",
					CertFile: filepath.Join(t.TempDir(), "missing.crt"),
					KeyFile:  filepath.Join(t.TempDir(), "missing.key"),
				},
			}
			if s.applyConfigUpdate(ctx, pending, computeConfigHash(pending)) {
				t.Fatal("故障注入未生效")
			}
			previousFailure := s.failedRuntimeHash
			switch action {
			case "full":
				s.applyUserUpdate(ctx, []model.UserSpec{{ID: 1}, {ID: 2}, {ID: 3}}, "")
			case "add":
				s.applyUserDelta(ctx, "add", []model.UserSpec{{ID: 3}})
			case "remove":
				s.applyUserDelta(ctx, "remove", []model.UserSpec{{ID: 2}})
			case "remove_last":
				s.applyUserDelta(ctx, "remove", []model.UserSpec{{ID: 1}, {ID: 2}})
			case "uuid":
				id, err := uuid.NewV4()
				if err != nil {
					t.Fatal(err)
				}
				s.applyUserDelta(ctx, "add", []model.UserSpec{{ID: 2, UUID: id.String()}})
			}
			if k.running || k.startCalls != 0 || s.runtimeError == nil || !strings.Contains(s.runtimeError.Error(), "certificate configuration") {
				t.Fatalf("用户变化绕过了无效证书: running=%v starts=%d error=%v", k.running, k.startCalls, s.runtimeError)
			}
			if previousFailure == s.failedRuntimeHash || !s.runtimeAttemptBlocked(s.lastConfig, s.lastUsers) {
				t.Fatal("未记录最新失败快照，可能重复重试")
			}
			if action == "remove_last" && s.lastUsers == nil {
				t.Fatal("删除最后一个用户丢失了明确的空集合")
			}
			corrected := *pending
			corrected.TLS = 0
			corrected.CertConfig = &config.CertConfig{CertMode: "none"}
			if !s.applyConfigUpdate(ctx, &corrected, computeConfigHash(&corrected)) || s.runtimeError != nil {
				t.Fatalf("证书配置修正后未恢复: %v", s.runtimeError)
			}
			if k.running != (len(s.lastUsers) > 0) {
				t.Fatal("修正后的运行状态与用户快照不符")
			}
		})
	}
}

func TestInvalidConfigSnapshotsReachRuntimeAndRecover(t *testing.T) {
	for _, source := range []string{"node", "machine"} {
		for _, mode := range []string{"initial", "poll", "ws"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				var corrected atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/api/v2/server/handshake":
						_, _ = w.Write([]byte(`{"websocket":{"enabled":false},"settings":{"push_interval":5,"pull_interval":1}}`))
					case "/api/v1/server/UniProxy/config":
						body := `{"protocol":"vless","server_port":12345,"custom_outbounds":[{"tag":"invalid","protocol":"direct","proxy_tag":"missing"}]}`
						if corrected.Load() {
							body = `{"protocol":"vless","server_port":12345}`
						}
						_, _ = w.Write([]byte(body))
					case "/api/v1/server/UniProxy/user":
						_, _ = w.Write([]byte(`{"users":[{"id":1},{"id":2}]}`))
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				k := &fakeKernel{running: mode != "initial"}
				s := newTestService(k)
				pcfg := config.PanelConfig{URL: server.URL, NodeID: 1}
				if source == "machine" {
					s.source = controlplane.NewMachinePanelControlPlane(panel.NewClient(pcfg), nil, nil)
				} else {
					s.source = controlplane.NewPanelControlPlane(pcfg, config.WSConfig{})
				}
				s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 12345}
				s.lastConfigHash = computeConfigHash(s.lastConfig)
				s.updateUserState([]model.UserSpec{{ID: 1}})
				ctx := context.Background()
				applySnapshot := func() {
					snapshot, err := s.source.Poll(ctx)
					if err != nil {
						t.Fatalf("配置快照未送达服务: %v", err)
					}
					if mode == "ws" {
						event := controlplane.TranslateWSEvent(panel.WSEvent{Type: panel.WSEventSyncConfig, Config: snapshot.Config.ToPanel()})
						if source == "machine" {
							if s.machineMailbox == nil {
								s.machineMailbox = controlplane.NewNodeMailbox()
								s.machineMailbox.SeedBaseline(s.lastUsers, s.lastConfig)
								s.machineMailbox.MarkReady()
							}
							s.machineMailbox.Apply(event)
							s.drainMachineMailbox(ctx)
						} else {
							s.handleWSEvent(ctx, event)
						}
					} else {
						s.applyPullResult(ctx, pullResult{config: snapshot.Config, configHash: computeConfigHash(snapshot.Config), users: snapshot.Users, userHash: computeUserHash(snapshot.Users)})
					}
				}
				if mode == "initial" {
					if err := s.initialSetup(ctx); err != nil {
						t.Fatalf("初始配置错误结束了控制通道: %v", err)
					}
				} else {
					applySnapshot()
				}
				if k.running || s.runtimeError == nil || !strings.Contains(s.runtimeError.Error(), "unknown outbound") || len(s.lastConfig.CustomOutbounds) != 1 {
					t.Fatalf("未停止内核并保留失败配置: running=%v error=%v", k.running, s.runtimeError)
				}
				applySnapshot()
				s.trackAndEnforce(ctx)
				if k.startCalls != 0 || k.reloadCalls != 0 {
					t.Fatal("重复失败配置或后台采样触发了内核应用")
				}
				s.applyUserUpdate(ctx, []model.UserSpec{{ID: 1}, {ID: 2}, {ID: 3}}, "")
				if k.running || s.runtimeError == nil || k.startCalls != 0 {
					t.Fatal("用户更新绕过了无效出站配置")
				}
				corrected.Store(true)
				applySnapshot()
				if !k.running || s.runtimeError != nil || k.startCalls != 1 {
					t.Fatalf("修正配置后未恢复: running=%v error=%v starts=%d", k.running, s.runtimeError, k.startCalls)
				}
			})
		}
	}
}

func TestLocalCertificateFailureKeepsInitialControlChannel(t *testing.T) {
	s := newTestService(&fakeKernel{})
	s.cfg.Standalone = &config.StandaloneConfig{}
	s.cfg.Standalone.Node.Protocol = "vless"
	s.cfg.Cert = config.CertConfig{CertMode: "file", CertFile: filepath.Join(t.TempDir(), "missing.crt")}
	s.source = controlplane.NewLocalControlPlane(s.cfg)
	if err := s.initialSetup(context.Background()); err != nil {
		t.Fatalf("证书错误结束了控制通道: %v", err)
	}
	if s.runtimeError == nil || s.kernel.IsRunning() {
		t.Fatal("本地证书错误未进入失败状态")
	}
}

func TestPollNetworkFailurePreservesRunningRuntime(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.source = &certRenewalPollSource{pollErr: errors.New("测试网络错误")}
	s.pullResults = make(chan pullResult, 1)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	previous := s.lastConfig
	s.pullViaAPIAsync(context.Background())
	deadline := time.Now().Add(time.Second)
	for s.pullActive.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.pullActive.Load() {
		t.Fatal("轮询未结束")
	}
	if !k.running || s.runtimeError != nil || s.lastConfig != previous || len(s.pullResults) != 0 {
		t.Fatal("未获取到新配置的网络错误不应停用已运行配置")
	}
}
