//go:build linux

package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func managedRealmFixture(t *testing.T) (*Manager, Policy) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "realm"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	m, err := New(t.TempDir(), "regression")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	rule := fixtureRule()
	rule.ListenPort, rule.Network, rule.Target.Address = port, "tcp", "1.1.1.1"
	m.run = func(context.Context, string, []string, string) ([]byte, error) { return []byte(`{"nftables":[]}`), nil }
	return m, Policy{Version: 1, Rules: []Rule{rule}}
}

func TestRealmNATConflictStopsExistingForwarding(t *testing.T) {
	m, p := managedRealmFixture(t)
	if status := m.Apply(t.Context(), p); len(status) != 1 || status[0].State != "running" {
		t.Fatal(status)
	}
	old := m.realms[1]
	m.run = func(context.Context, string, []string, string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"nftables":[{"rule":{"family":"ip","table":"other","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":%d}},{"dnat":{"addr":"1.0.0.1"}}]}}]}`, p.Rules[0].ListenPort)), nil
	}
	m.natCheckedAt = time.Time{}
	status := m.Apply(t.Context(), p)
	if len(status) != 1 || status[0].State != "failed" || old.alive() {
		t.Fatal("NAT collision retained a healthy status/process", status)
	}
}

func TestRealmNATInspectionUnavailableReportsWarning(t *testing.T) {
	m, p := managedRealmFixture(t)
	m.run = func(context.Context, string, []string, string) ([]byte, error) {
		return nil, errors.New("operation not permitted")
	}
	status := m.Apply(t.Context(), p)
	if len(status) != 1 || status[0].State != "running" || status[0].Warning == "" {
		t.Fatal(status)
	}
}

func TestUnchangedForwardingAvoidsRepeatedExternalInspection(t *testing.T) {
	m, p := managedRealmFixture(t)
	calls := 0
	m.run = func(context.Context, string, []string, string) ([]byte, error) {
		calls++
		return []byte(`{"nftables":[]}`), nil
	}
	for range 3 {
		if status := m.Apply(t.Context(), p); status[0].State != "running" {
			t.Fatal(status)
		}
	}
	if calls != 1 {
		t.Fatal("repeated external inspections", calls)
	}
}

func TestRealmTemporaryDNSPreservesProcessUntilGraceExpires(t *testing.T) {
	m, p := managedRealmFixture(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	p.Rules[0].Target.Address = "target.test"
	if status := m.Apply(t.Context(), p); status[0].State != "running" {
		t.Fatal(status)
	}
	old := m.realms[1]
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "timeout", IsTimeout: true}
	}
	now = now.Add(20 * time.Second)
	if status := m.Apply(t.Context(), p); status[0].State != "running" || !old.alive() || old != m.realms[1] {
		t.Fatal(status)
	}
	now = now.Add(41 * time.Second)
	if status := m.Apply(t.Context(), p); status[0].State != "failed" || old.alive() {
		t.Fatal("expired target remained active", status)
	}
}
