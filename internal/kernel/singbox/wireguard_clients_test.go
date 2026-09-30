//go:build with_quic && with_wireguard && with_mihomo_integration

package singbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/testing/realitytest"
)

// 显式启用此测试时，必须提供独立运行的 Mihomo 程序路径；不使用桌面客户端配置。
func TestWireGuardRealityClients(t *testing.T) {
	binary := os.Getenv("WG_MIHOMO_BIN")
	if binary == "" {
		t.Fatal("WG_MIHOMO_BIN 未设置")
	}
	// 复用核心的 REALITY 测试站点，避免普通 HTTPS 站点的证书长度和探测等待影响结果。
	fixture := realitytest.Start(t, true)
	shortID := hex.EncodeToString(fixture.ShortID[:])
	publicKey := base64.RawURLEncoding.EncodeToString(fixture.PublicKey)
	sender, receiver := runtimeWireGuardPair(t)
	landing := runtimeNode(t, "wireguard")
	landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "wireguard", WireGuard: receiver}
	relayStartCore(t, "singbox", landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "wg"))
	node := runtimeNode(t, "vless")
	node.TLS = 2
	node.TLSSettings = map[string]any{"server_name": "reality.test", "private_key": base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey), "short_id": shortID}
	node.TLSSettings["dest"] = fixture.Config.Dest
	node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{NodeID: 7, RouteID: 12, Tag: "relay-7", Protocol: "wireguard", Address: "127.0.0.1", Port: landing.ServerPort, WireGuard: sender}}}
	user := runtimeUser(t, 810)
	relayStartCore(t, "xray", node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
	user.UUID = relayCredential(user, 12)
	singClient := runtimeClientWithTLS(t, node, user, map[string]any{"enabled": true, "server_name": "reality.test", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": publicKey, "short_id": shortID}})
	for _, network := range []string{"tcp", "udp"} {
		relayMarkerExchange(t, singClient, "wg", network)
	}
	proxyNode := runtimeNode(t, "socks")
	cfg := map[string]any{
		"mixed-port": proxyNode.ServerPort, "bind-address": "127.0.0.1", "allow-lan": false, "mode": "rule", "log-level": "debug",
		"proxies": []map[string]any{{"name": "wg-route", "type": "vless", "server": "127.0.0.1", "port": node.ServerPort, "uuid": user.UUID, "network": "tcp", "tls": true, "udp": true, "servername": "reality.test", "client-fingerprint": "chrome", "reality-opts": map[string]any{"public-key": publicKey, "short-id": shortID}}},
		"rules":   []string{"MATCH,wg-route"},
	}
	dir := t.TempDir()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "-d", dir, "-f", path)
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
		if t.Failed() {
			message := strings.NewReplacer(user.UUID, "[测试身份]", publicKey, "[测试公钥]").Replace(output.String())
			if len(message) > 4000 {
				message = message[len(message)-4000:]
			}
			t.Logf("Mihomo 失败日志: %s", message)
		}
	})
	ready := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyNode.ServerPort)), time.Second)
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		_ = process.Process.Kill()
		_ = process.Wait()
		message := strings.NewReplacer(user.UUID, "[测试身份]", publicKey, "[测试公钥]").Replace(output.String())
		if len(message) > 2000 {
			message = message[:2000]
		}
		t.Fatalf("隔离 Mihomo 客户端未就绪: %s", message)
	}
	client := runtimeClient(t, proxyNode, model.UserSpec{})
	for _, network := range []string{"tcp", "udp"} {
		relayMarkerExchange(t, client, "wg", network)
	}
}
