//go:build with_quic && with_utls

package singbox

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xkernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
	"github.com/xtls/xray-core/testing/realitytest"
)

func TestHealthListenerInspectionKeepsRealityLogsForRealConnections(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, private := range []bool{false, true} {
			mode := "normal"
			if private {
				mode = "private"
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				scope := t.Name()
				dir := t.TempDir()
				state := privacytest.Bind(t, dir)
				nlog.ConfigureLogPolicies([]string{scope})
				nlog.RegisterLogScope(scope, scope, dir+"/runtime.log.enc", state)
				nlog.SetGlobalScope(scope)
				nlog.ApplyLogPolicy(scope, private)
				nlog.Init(io.Discard, slog.LevelInfo, false)
				t.Cleanup(func() {
					nlog.ConfigureLogPolicies(nil)
					nlog.SetGlobalScope("")
					nlog.Init(os.Stdout, slog.LevelInfo, false)
				})
				events := make(chan nlog.Event, 128)
				stop := nlog.Subscribe(scope, func(event nlog.Event) {
					if event.Source == kind && (event.Level == "error" || event.Level == "fatal") {
						events <- event
					}
				})
				t.Cleanup(stop)
				fixture := realitytest.Start(t, true)
				node := runtimeNode(t, "vless")
				node.TLS = 2
				node.TLSSettings = map[string]any{
					"server_name": "reality.test",
					"private_key": base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey.Bytes()),
					"short_id":    hex.EncodeToString(fixture.ShortID[:]),
					"dest":        fixture.Config.Dest,
				}
				cfg := config.KernelConfig{LogLevel: "error", LogScope: scope, LogNodeID: 998, ConfigDir: dir}
				var core kernel.Kernel
				if kind == "singbox" {
					core = New(cfg)
				} else {
					node.CustomRoutes = nil
					core = xkernel.New(cfg)
				}
				if err := core.Start(node, []model.UserSpec{runtimeUser(t, 998)}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				address := net.JoinHostPort("127.0.0.1", strconv.Itoa(node.ServerPort))
				for range 3 {
					if ok, err := healthcheck.Listener(t.Context(), address); !ok || err != nil {
						t.Fatalf("未确认真实 Reality 监听：%v %v", ok, err)
					}
				}
				select {
				case event := <-events:
					t.Fatalf("监听检查制造了运行错误：%s", event.Message)
				case <-time.After(25 * time.Millisecond):
				}
				if kind == "singbox" {
					// 对照：真实的无效连接仍须记录，不能全局屏蔽 Reality 错误。
					conn, err := net.DialTimeout("tcp", address, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					conn.Close()
					select {
					case event := <-events:
						if event.Level != "error" || !strings.Contains(event.Message, "REALITY: processed invalid connection") {
							t.Fatalf("真实握手错误未正常记录：%+v", event)
						}
					case <-time.After(time.Second):
						t.Fatal("真实连接错误被健康检查修复隐藏")
					}
				}
				core.Stop()
				if ok, err := healthcheck.Listener(t.Context(), address); ok || err != nil {
					t.Fatalf("停止的核心仍被判为监听正常：%v %v", ok, err)
				}
			})
		}
	}
}
