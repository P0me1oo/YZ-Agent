package singbox

import (
	"reflect"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestLegacyDirectOutboundReloadMode(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, other, nativeBind, nativeStrategy string
	}{
		{"ipv4", "127.0.0.2", "127.0.0.1", "::1", "inet4_bind_address", "ipv4_only"},
		{"ipv6", "::1", "::1", "127.0.0.1", "inet6_bind_address", "ipv6_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := runtimeNode(t, "shadowsocks2022-128")
			native.CustomRoutes = []M{{"ip_cidr": []string{"127.0.0.0/8", "::1/128"}, "outbound": "direct"}}
			native.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: M{
				tc.nativeBind: tc.source, "domain_strategy": tc.nativeStrategy,
			}}}
			legacy := *native
			legacy.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: M{
				"sendThrough": tc.source,
			}}}
			cfg := config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()}
			user := runtimeUser(t, 9301)
			users := []model.UserSpec{user}
			// 生成字段完全相同，重载仍须识别旧式绑定附带的单地址限制。
			if !reflect.DeepEqual(mustBuildConfig(t, cfg, native, users, kernel.TLSCert{}), mustBuildConfig(t, cfg, &legacy, users, kernel.TLSCert{})) {
				t.Fatal("本例必须覆盖生成配置相同但绑定语义不同的情况")
			}
			s := New(cfg)
			t.Cleanup(s.Stop)
			if err := s.Start(native, users, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			client := runtimeClient(t, native, user)
			target, sources := directCompatTCPEcho(t, tc.target)
			other, otherSources := directCompatTCPEcho(t, tc.other)
			for _, node := range []*model.NodeSpec{native, &legacy, &legacy, native} {
				if err := s.Reload(node, users, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				conn := runtimeDial(t, client, target)
				runtimeExchange(t, conn)
				_ = conn.Close()
				directCompatCheckSource(t, sources, tc.source)
				if node == &legacy {
					runtimeReject(t, client, other)
					select {
					case <-otherSources:
						t.Fatal("切换为单地址绑定后，另一地址族仍被使用")
					default:
					}
				} else {
					conn := runtimeDial(t, client, other)
					runtimeExchange(t, conn)
					_ = conn.Close()
					directCompatCheckSource(t, otherSources, tc.other)
				}
			}
		})
	}
}

func TestLegacyDirectOutboundInvalidConfigRecovery(t *testing.T) {
	valid := runtimeNode(t, "shadowsocks2022-128")
	valid.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: M{
		"domainStrategy": "ForceIPv4", "send_through": "127.0.0.2",
	}}}
	invalid := *valid
	invalid.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: M{
		"domainStrategy": "ForceIPv6", "send_through": "127.0.0.2",
	}}}
	user := runtimeUser(t, 9302)
	users := []model.UserSpec{user}
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	checkError := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "custom_outbounds[0]") || !strings.Contains(err.Error(), "domainStrategy") {
			t.Fatalf("未返回出站转换的准确原因: %v", err)
		}
	}
	for range 2 {
		checkError(s.Start(&invalid, users, kernel.TLSCert{}))
		if s.IsRunning() {
			t.Fatal("错误配置启动后不应报告运行中")
		}
	}
	if err := s.Start(valid, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	target, sources := directCompatTCPEcho(t, "127.0.0.1")
	client := runtimeClient(t, valid, user)
	for range 2 {
		conn := runtimeDial(t, client, target)
		runtimeExchange(t, conn)
		_ = conn.Close()
		directCompatCheckSource(t, sources, "127.0.0.2")
		checkError(s.Reload(&invalid, users, kernel.TLSCert{}))
		// 沿用服务层收到错误后停止内核的流程，随后只接受修正后的配置。
		s.Stop()
		assertListenerClosed(t, valid)
		if err := s.Start(valid, users, kernel.TLSCert{}); err != nil {
			t.Fatalf("修正直连参数后恢复失败: %v", err)
		}
	}
	conn := runtimeDial(t, client, target)
	runtimeExchange(t, conn)
	_ = conn.Close()
	directCompatCheckSource(t, sources, "127.0.0.2")
	want := int64(3 * len(runtimePayload))
	runtimeWaitTraffic(t, s, user.ID, [2]int64{want, want})
}
