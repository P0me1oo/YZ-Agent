//go:build with_quic

package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
)

// 显式提供 Mihomo 和面板路径时，使用面板实际的 HY2 订阅构建器做跨仓库验证。
// 客户端只监听回环地址；含认证信息的配置由测试临时目录自动清理。
func relayMihomoProbe(t *testing.T, binary string, node *model.NodeSpec, cert kernel.TLSCert, configPEM, auth string, probe func(adapter.Outbound)) {
	t.Helper()
	if os.Getenv("YZ_HY2_ECH_PANEL_ROOT") == "" {
		t.Fatal("Mihomo 联测必须同时设置 YZ_HY2_ECH_PANEL_ROOT")
	}
	server := map[string]any{
		"name": "ech-relay-probe", "host": "127.0.0.1", "port": node.ServerPort, "password": auth,
		"protocol_settings": map[string]any{
			"version": 2, "bandwidth": map[string]any{"up": 100, "down": 100},
			"tls": map[string]any{"server_name": "localhost", "allow_insecure": false,
				"ech": map[string]any{"enabled": true, "config": configPEM}},
			"obfs": map[string]any{"open": node.Obfs != "", "type": node.Obfs, "password": node.ObfsPassword},
		},
	}
	data, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	php := exec.Command("php", "-r", `require getenv('YZ_HY2_ECH_PANEL_ROOT') . '/vendor/autoload.php';
$server = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
echo json_encode(\App\Protocols\ClashMeta::buildHysteria($server['password'], $server, []), JSON_THROW_ON_ERROR);`)
	php.Stdin = bytes.NewReader(data)
	proxyJSON, err := php.Output()
	if err != nil {
		t.Fatalf("面板 HY2 订阅参数生成失败: %v", err)
	}
	var proxy map[string]any
	if err := json.Unmarshal(proxyJSON, &proxy); err != nil {
		t.Fatal("面板未返回有效的代理参数")
	}
	proxy["ca-str"] = string(cert.CertPEM)
	clientNode := runtimeNode(t, "socks")
	data, err = json.Marshal(map[string]any{
		"socks-port": clientNode.ServerPort, "allow-lan": false, "bind-address": "127.0.0.1",
		"mode": "rule", "log-level": "silent", "ipv6": false,
		"proxies": []any{proxy}, "rules": []string{"MATCH,ech-relay-probe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mihomo.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-d", dir, "-f", configPath)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("无法启动 Mihomo 测试进程: %v", err)
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("Mihomo 在测试前退出: %v", runErr)
		default:
		}
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", clientNode.ServerPort), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Mihomo 测试监听未就绪")
		}
		time.Sleep(20 * time.Millisecond)
	}
	probe(runtimeClient(t, clientNode, model.UserSpec{}))
}
