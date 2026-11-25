package portforward

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func resolutionManager(t *testing.T) (*Manager, Policy, *time.Time) {
	t.Helper()
	m, err := New(t.TempDir(), "resolution")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	now := time.Now()
	m.now = func() time.Time { return now }
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return m, Policy{Version: 1, Rules: []Rule{fixtureRule()}}, &now
}

func TestForwardingDNSGraceIsBoundedAndDoesNotAuthorizeChangedPolicy(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[change], func(t *testing.T) {
			m, policy, now := resolutionManager(t)
			initial, _ := m.resolveDesired(t.Context(), policy, nil)
			if len(initial) != 1 {
				t.Fatal("initial target rejected")
			}
			*now = now.Add(20 * time.Second)
			m.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return nil, &net.DNSError{Err: "temporary failure", IsTemporary: true}
			}
			if change {
				policy.Ingress = append(policy.Ingress, Endpoint{Address: "new.test", Port: 443})
			}
			got, status := m.resolveDesired(t.Context(), policy, nil)
			if change {
				if len(got) != 0 || status[0].State != "failed" {
					t.Fatal("changed policy reused old authorization")
				}
				return
			}
			if len(got) != 1 || status[0].Warning == "" || got[1].IP != initial[1].IP {
				t.Fatal(got, status)
			}
			*now = now.Add(41 * time.Second)
			got, status = m.resolveDesired(t.Context(), policy, nil)
			if len(got) != 0 || status[0].State != "failed" {
				t.Fatal("failed DNS extended grace indefinitely")
			}
		})
	}
}

func TestForwardingUnsafeDNSNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		notFound      bool
	}{
		{"loopback", "127.0.0.1", false},
		{"metadata", "169.254.169.254", false},
		{"local-public", "1.1.1.1", false},
		{"nxdomain", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, policy, now := resolutionManager(t)
			m.resolveDesired(t.Context(), policy, nil)
			*now = now.Add(20 * time.Second)
			m.lookup = func(context.Context, string) ([]netip.Addr, error) {
				if tc.notFound {
					return nil, &net.DNSError{Err: "not found", IsNotFound: true}
				}
				return []netip.Addr{netip.MustParseAddr(tc.address)}, nil
			}
			local := map[netip.Addr]bool{}
			if tc.name == "local-public" {
				local[netip.MustParseAddr(tc.address)] = true
			}
			got, status := m.resolveDesired(t.Context(), policy, local)
			if len(got) != 0 || status[0].State != "failed" {
				t.Fatal("unsafe or removed target fell back", got, status)
			}
		})
	}
}

func TestForwardingResolutionReusesFreshResult(t *testing.T) {
	m, policy, now := resolutionManager(t)
	calls := 0
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	m.resolveDesired(t.Context(), policy, nil)
	*now = now.Add(5 * time.Second)
	m.resolveDesired(t.Context(), policy, nil)
	if calls != 1 {
		t.Fatal("unchanged policy repeated DNS before refresh", calls)
	}
	*now = now.Add(11 * time.Second)
	m.resolveDesired(t.Context(), policy, nil)
	if calls != 2 {
		t.Fatal("DNS never refreshed", calls)
	}
}

func TestForwardingLocalTargetBoundary(t *testing.T) {
	m, _, _ := resolutionManager(t)
	for _, address := range []string{"127.0.0.1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "::1", "fd00::1", "64:ff9b::a00:1"} {
		if m.allowsTarget(netip.MustParseAddr(address), 443, nil) {
			t.Fatal("private target allowed", address)
		}
	}
	m.targets = []TargetPermission{{Prefix: netip.MustParsePrefix("127.0.0.1/32"), Ports: []int{443}}}
	if !m.allowsTarget(netip.MustParseAddr("::ffff:127.0.0.1"), 443, nil) {
		t.Fatal("explicit local authorization rejected")
	}
	if m.allowsTarget(netip.MustParseAddr("127.0.0.1"), 22, nil) {
		t.Fatal("authorization escaped port boundary")
	}
	if !m.allowsTarget(netip.MustParseAddr("1.1.1.1"), 443, nil) {
		t.Fatal("public target rejected")
	}
}
