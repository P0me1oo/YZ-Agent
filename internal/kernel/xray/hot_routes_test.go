package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"net"
	"testing"
)

func TestHotRoutesRetainsActualInstance(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	node := &model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: port}
	users := []model.UserSpec{{ID: 1, UUID: "fbdbb984-3b9e-4acd-a736-8cc1b90a9d4d"}}
	k := New(config.KernelConfig{})
	if err := k.Start(node, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	initial := k.instance
	next := *node
	next.CustomRoutes = []map[string]any{{"type": "field", "domain": []string{"example.com"}, "outboundTag": "block"}}
	if err := k.Reload(&next, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	if k.instance != initial {
		t.Fatal("route update recreated core")
	}
	if err := k.Reload(&next, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	if k.instance != initial {
		t.Fatal("duplicate update recreated core")
	}
}
func TestRouteDiffRejectsStaticAndOutboundChanges(t *testing.T) {
	before := M{"routing": M{"domainStrategy": "AsIs", "rules": []M{}}, "outbounds": []M{{"tag": "direct"}}}
	after := M{"routing": M{"domainStrategy": "IPIfNonMatch", "rules": []M{}}, "outbounds": []M{{"tag": "direct"}}}
	if routesOnly(before, after) {
		t.Fatal("static routing change hot applied")
	}
	before = M{"routing": M{}, "outbounds": []M{{"tag": "direct"}}}
	after = M{"routing": M{}, "outbounds": []M{{"tag": "proxy"}}}
	if routesOnly(before, after) {
		t.Fatal("outbound change hot applied")
	}
}
