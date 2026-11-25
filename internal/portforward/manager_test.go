//go:build linux

package portforward

import (
	"context"
	"errors"
	"github.com/vishvananda/netlink"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCachedPolicyAndExclusiveOwnership(t *testing.T) {
	dir := t.TempDir()
	m, e := New(dir, "test")
	if e != nil {
		t.Fatal(e)
	}
	p := Policy{Version: 1, Rules: []Rule{fixtureRule()}}
	if e = m.Save(p); e != nil {
		t.Fatal(e)
	}
	if _, e = New(dir, "test"); e == nil {
		t.Fatal("second owner accepted")
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	m, e = New(dir, "test")
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	loaded, e := m.Load()
	if e != nil || len(loaded.Rules) != 1 {
		t.Fatal(loaded, e)
	}
}
func TestNFTIsolatedTransactionAndForeignOwnership(t *testing.T) {
	m, e := New(t.TempDir(), "test")
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	r := resolvedRule{fixtureRule(), netip.MustParseAddr("192.0.2.2")}
	r.Mode = "nftables"
	script := nftScript(m.table, []resolvedRule{r}, true)
	if strings.Contains(script, "flush ruleset") || !strings.Contains(script, "delete table inet "+m.table) || !strings.Contains(script, "fib daddr type local") || !strings.Contains(script, "ct original proto-dst") {
		t.Fatal(script)
	}
	if e = os.WriteFile(filepath.Join(m.dir, "nft-owned"), []byte(m.table), 0600); e != nil {
		t.Fatal(e)
	}
	mutations := 0
	m.run = func(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
		if strings.Join(args, " ") == "-j list tables" {
			return []byte(`{"nftables":[{"table":{"family":"inet","name":"` + m.table + `"}}]}`), nil
		}
		if len(args) > 2 && (args[1] == "list" || args[0] == "list") {
			return []byte(`{"nftables":[{"table":{"comment":"somebody else"}}]}`), nil
		}
		mutations++
		return nil, errors.New("unexpected mutation")
	}
	if err := m.applyNFT(t.Context(), nil); err == nil || mutations != 0 {
		t.Fatal("foreign table modified", err, mutations)
	}
	os.Remove(filepath.Join(m.dir, "nft-owned"))
}
func TestMissingRealmRetriesWithoutPanic(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m, e := New(t.TempDir(), "test")
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}
	p := Policy{Version: 1, Rules: []Rule{fixtureRule()}}
	for range 2 {
		s := m.Apply(t.Context(), p)
		if len(s) != 1 || s[0].State != "failed" {
			t.Fatal(s)
		}
	}
}

func TestNFTDetectsOtherOwnerPorts(t *testing.T) {
	r := resolvedRule{fixtureRule(), netip.MustParseAddr("192.0.2.2")}
	for _, tt := range []struct {
		name, right string
		conflict    bool
	}{{"same", "18081", true}, {"different", "443", false}, {"range", `{"range":[18000,19000]}`, true}, {"set", `{"set":[443,18081]}`, true}} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"nftables":[{"rule":{"family":"ip","table":"other","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":` + tt.right + `}},{"dnat":{"addr":"192.0.2.3"}}]}}]}`)
			if e := nftConflicts(raw, "ours", []resolvedRule{r}); (e != nil) != tt.conflict {
				t.Fatal(e)
			}
		})
	}
}

func TestConnectionCleanupMatchesOnlyManagedForwarding(t *testing.T) {
	r := resolvedRule{fixtureRule(), netip.MustParseAddr("192.0.2.2")}
	f := forwardingFlowFilter{r, map[netip.Addr]bool{netip.MustParseAddr("192.0.2.1"): true}}
	flow := &netlink.ConntrackFlow{Forward: netlink.IPTuple{DstIP: net.ParseIP("192.0.2.1"), DstPort: uint16(r.ListenPort), Protocol: 17}, Reverse: netlink.IPTuple{SrcIP: net.ParseIP("192.0.2.2"), SrcPort: uint16(r.Target.Port)}}
	if !f.MatchConntrackFlow(flow) {
		t.Fatal("managed mapping missed")
	}
	flow.Reverse.SrcPort++
	if f.MatchConntrackFlow(flow) {
		t.Fatal("other target affected")
	}
	flow.Reverse.SrcPort--
	flow.Forward.DstIP = net.ParseIP("192.0.2.3")
	if f.MatchConntrackFlow(flow) {
		t.Fatal("unrelated forwarded mapping affected")
	}
}

func TestOldNFTTextOwnership(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want       bool
	}{
		{"owned", "table inet test {\n comment \"yz-agent-portforward-v1:test\"\n chain prerouting {\n }\n}", true},
		{"foreign", "table inet test {\n comment \"other\"\n}", false},
		{"rule-comment", "table inet test {\n chain prerouting {\n comment \"yz-agent-portforward-v1:test\"\n }\n}", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if ownsTextTable(tt.text, "test") != tt.want {
				t.Fatal(tt.text)
			}
		})
	}
}
