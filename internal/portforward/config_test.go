package portforward

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

func fixtureRule() Rule {
	return Rule{ID: 1, Mode: "realm", ListenPort: 18081, Target: Endpoint{"target.test", 18082}, Network: "tcp,udp"}
}
func TestPolicyRejectsInvalidAndDuplicateRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Policy)
	}{
		{"port", func(p *Policy) { p.Rules[0].ListenPort = 65536 }},
		{"injection", func(p *Policy) { p.Rules[0].Target.Address = "127.0.0.1;flush ruleset" }},
		{"duplicate", func(p *Policy) { p.Rules = append(p.Rules, p.Rules[0]) }},
		{"transport", func(p *Policy) { p.Rules[0].Network = "sctp" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{Version: 1, Rules: []Rule{fixtureRule()}}
			tt.mutate(&p)
			if p.Validate() == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
func TestResolveRejectsDNSLoopAndPinsIP(t *testing.T) {
	r := fixtureRule()
	p := Policy{Version: 1, Rules: []Rule{r}, Ingress: []Endpoint{{"entry.test", r.Target.Port}}}
	lookup := func(_ context.Context, s string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}
	if _, err := resolveRule(t.Context(), r, p, nil, lookup); err == nil {
		t.Fatal("DNS alias loop accepted")
	}
	p.Ingress = nil
	got, err := resolveRule(t.Context(), r, p, nil, lookup)
	if err != nil || got.IP.String() != "192.0.2.1" {
		t.Fatal(got, err)
	}
	r.Target.Port = r.ListenPort
	p.Rules = []Rule{r}
	if _, err := resolveRule(t.Context(), r, p, map[netip.Addr]bool{got.IP: true}, lookup); err == nil {
		t.Fatal("local loop accepted")
	}
}
func TestPortConflictTCPAndUDP(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			r := fixtureRule()
			r.Network = network
			if network == "tcp" {
				listener, e := net.Listen("tcp4", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				defer listener.Close()
				r.ListenPort = listener.Addr().(*net.TCPAddr).Port
			} else {
				listener, e := net.ListenPacket("udp4", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				defer listener.Close()
				r.ListenPort = listener.LocalAddr().(*net.UDPAddr).Port
			}
			if checkPort(r) == nil {
				t.Fatal("occupied port accepted")
			}
		})
	}
}
