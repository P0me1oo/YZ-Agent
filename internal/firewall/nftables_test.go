package firewall

import (
	"fmt"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

func TestNFTRecognizesOwnershipWithoutTableComment(t *testing.T) {
	b := &systemBackend{scope: "a1b2c3d4e5f6"}
	rule := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 20000, To: 20100}, Target: 8443}
	output := fmt.Sprintf(`{"nftables":[
{"table":{"family":"inet","name":"%s"}},
{"chain":{"name":"ownership"}},
{"rule":{"chain":"ownership","comment":"%s"}},
{"chain":{"name":"prerouting","type":"nat","hook":"prerouting","prio":-100,"policy":"accept"}},
{"rule":{"chain":"prerouting","comment":"%s"}}
]}`, b.nftTable(), b.nftOwner(), redirectComment(rule))
	if matches, err := b.checkNFTTable(output, []Rule{rule}); err != nil || !matches {
		t.Fatalf("兼容 nftables 1.0.6 的归属校验失败：%v", err)
	}
	foreign := strings.Replace(output, b.nftOwner(), "manual-owner", 1)
	if _, err := b.checkNFTTable(foreign, nil); err == nil {
		t.Fatal("不能删除同名的非托管表")
	}
	withSet := strings.Replace(output, `{"table":`, `{"set":{"name":"manual-set"}},{"table":`, 1)
	if _, err := b.checkNFTTable(withSet, nil); err == nil {
		t.Fatal("不能删除包含非托管集合的表")
	}
}
