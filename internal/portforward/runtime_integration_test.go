//go:build integration && linux

package portforward

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRealmRuntimeLifecycle(t *testing.T) {
	if _, err := exec.LookPath("realm"); err != nil {
		t.Skip("Realm required")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenPacket("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	go func() {
		b := make([]byte, 4096)
		for {
			n, a, e := udp.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = udp.WriteTo(b[:n], a)
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	forward := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	localTarget := TargetPermission{Prefix: netip.MustParsePrefix("127.0.0.1/32"), Ports: []int{port, port + 1}}
	m, err := New(t.TempDir(), "realm-runtime", localTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := Policy{Version: 1, Rules: []Rule{{ID: 1, Mode: "realm", ListenPort: forward, Target: Endpoint{"127.0.0.1", port}, Network: "tcp,udp"}}}
	probe := func(network string) {
		t.Helper()
		c, e := net.DialTimeout(network, net.JoinHostPort("127.0.0.1", strconv.Itoa(forward)), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		input := bytes.Repeat([]byte("forwarding"), 100)
		if _, e = c.Write(input); e != nil {
			t.Fatal(e)
		}
		output := make([]byte, len(input))
		if _, e = io.ReadFull(c, output); e != nil || !bytes.Equal(input, output) {
			t.Fatal("echo mismatch", e)
		}
	}
	apply := func() {
		t.Helper()
		s := m.Apply(ctx, p)
		if len(s) != 1 || s[0].State != "running" {
			t.Fatal(s)
		}
	}
	apply()
	probe("tcp4")
	probe("udp4")
	pid := m.realms[1].cmd.Process.Pid
	apply()
	if m.realms[1].cmd.Process.Pid != pid {
		t.Fatal("unchanged config restarted Realm")
	}
	_ = m.realms[1].cmd.Process.Kill()
	<-m.realms[1].done
	apply()
	probe("tcp4")
	probe("udp4")
	if err = m.Save(p); err != nil {
		t.Fatal(err)
	}
	dir := m.dir
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if c, e := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(forward)), 100*time.Millisecond); e == nil {
		c.Close()
		t.Fatal("stopped listener still available")
	}
	// Recreate exactly the same manager identity after restart.
	m, err = New(filepath.Dir(dir), "realm-runtime", localTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	apply()
	probe("tcp4")
	probe("udp4")
	p.Rules[0].Target.Port = port + 1
	apply()
	p.Rules = nil
	if s := m.Apply(ctx, p); len(s) != 0 {
		t.Fatal(s)
	}
	if len(m.realms) != 0 {
		t.Fatal("deleted process retained")
	}
}

func TestNFTNativeIsolated(t *testing.T) {
	if os.Getenv("YZ_FORWARD_NETNS_TEST") != "1" {
		t.Skip("requires disposable network namespace")
	}
	m, err := New(t.TempDir(), "native", TargetPermission{Prefix: netip.MustParsePrefix("192.0.2.2/32"), Ports: []int{9443}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	run := func(args ...string) {
		t.Helper()
		if output, e := exec.Command("nft", args...).CombinedOutput(); e != nil {
			t.Fatal(string(output), e)
		}
	}
	run("add", "table", "inet", "unrelated_test")
	defer exec.Command("nft", "delete", "table", "inet", "unrelated_test").Run()
	p := Policy{Version: 1, Rules: []Rule{{ID: 1, Mode: "nftables", ListenPort: 18443, Target: Endpoint{"192.0.2.2", 9443}, Network: "tcp,udp"}}}
	m.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, nil
	}
	for range 2 {
		if s := m.Apply(t.Context(), p); len(s) != 1 || s[0].State != "running" {
			t.Fatal(s)
		}
	}
	run("list", "table", "inet", "unrelated_test")
	run("delete", "table", "inet", m.table)
	m.nftCheckedAt = time.Time{}
	if s := m.Apply(t.Context(), p); len(s) != 1 || s[0].State != "running" {
		t.Fatal(s)
	}
	p.Rules = nil
	if s := m.Apply(t.Context(), p); len(s) != 0 {
		t.Fatal(s)
	}
	run("list", "table", "inet", "unrelated_test")
	if exec.Command("nft", "list", "table", "inet", m.table).Run() == nil {
		t.Fatal("table survived delete")
	}
}
