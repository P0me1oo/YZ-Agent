package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
)

// 实际启动两种核心，验证误推用户、重载和停止后恢复不会关闭落地监听。
func TestLandingRuntimeSurvivesUserSync(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"shadowsocks", "vless"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				socket, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := socket.Addr().(*net.TCPAddr).Port
				socket.Close()
				key := make([]byte, 16)
				if _, err := rand.Read(key); err != nil {
					t.Fatal(err)
				}
				nc := landingConfig()
				nc.Protocol, nc.ListenIP, nc.ServerPort = protocol, "127.0.0.1", port
				nc.Network = "tcp"
				nc.Relay.Protocol, nc.Relay.ListenPort = protocol, port
				nc.Relay.Password = base64.StdEncoding.EncodeToString(key)
				if protocol == "vless" {
					nc.Relay.VLESS = &model.RelayVLESSConfig{ID: randomRelayTestID(), Network: "tcp", Encryption: "none"}
				}
				cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
				var core kernel.Kernel
				if kind == "singbox" {
					core = singbox.New(cfg)
				} else {
					core = xray.New(cfg)
				}
				t.Cleanup(core.Stop)
				s := newTestService(&fakeKernel{})
				s.kernel, s.cfg.Kernel, s.lastConfig = core, cfg, nc
				ctx := context.Background()
				users := []model.UserSpec{{ID: 9, UUID: randomRelayTestID()}}
				check := func() {
					t.Helper()
					conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
					if err != nil {
						t.Fatalf("落地监听不可用: %v", err)
					}
					conn.Close()
					if !core.IsRunning() || s.runtimeError != nil || len(s.lastUsers) != 0 {
						t.Fatal("落地状态异常")
					}
				}
				if !s.ensureRunning(ctx) {
					t.Fatalf("启动失败: %v", s.runtimeError)
				}
				check()
				for range 2 {
					if !s.applyUserDelta(ctx, "add", users) || !s.applyUserUpdate(ctx, users, computeUserHash(users)) ||
						!s.applyUserDelta(ctx, "remove", users) || !s.applyUserUpdate(ctx, nil, computeUserHash(nil)) {
						t.Fatalf("用户同步失败: %v", s.runtimeError)
					}
					check()
					if !s.applyConfigUpdate(ctx, nc, computeConfigHash(nc)) {
						t.Fatalf("重载失败: %v", s.runtimeError)
					}
					check()
				}
				core.Stop()
				if !s.applyUserUpdate(ctx, users, computeUserHash(users)) {
					t.Fatalf("恢复失败: %v", s.runtimeError)
				}
				check()
			})
		}
	}
}

func randomRelayTestID() string {
	// 凭据仅在测试进程内生成，不落盘。
	return uuid.Must(uuid.NewV4()).String()
}
