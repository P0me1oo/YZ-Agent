//go:build linux

package portforward

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type commandFunc func(context.Context, string, []string, string) ([]byte, error)

func runCommand(ctx context.Context, name string, args []string, input string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		return output.Bytes(), fmt.Errorf("%s failed: %w", name, err)
	}
	return output.Bytes(), nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 4<<20 {
		_, _ = b.Buffer.Write(p[:min(n, (4<<20)-b.Len())])
	}
	return n, nil
}
func ipv6Unavailable(err error) bool {
	return errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)
}
func lockDirectory(dir string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(dir, "manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("forwarding manager already running")
	}
	return f, nil
}

type realmProcess struct {
	rule resolvedRule
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *realmProcess) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *realmProcess) stop() {
	if !p.alive() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}
func (m *Manager) startRealm(ctx context.Context, r resolvedRule) (*realmProcess, error) {
	path, err := exec.LookPath("realm")
	if err != nil {
		return nil, errors.New("Realm executable is not installed")
	}
	// Always use the validated numeric target so DNS cannot change behind the loop guard.
	config := map[string]any{"log": map[string]any{"level": "error"}, "endpoints": []any{map[string]any{
		"listen": net.JoinHostPort("::", strconv.Itoa(r.ListenPort)), "remote": net.JoinHostPort(r.IP.String(), strconv.Itoa(r.Target.Port)),
		"network": map[string]any{"no_tcp": r.Network == "udp", "use_udp": strings.Contains(r.Network, "udp"), "ipv6_only": false},
	}}}
	raw, _ := json.Marshal(config)
	file := filepath.Join(m.dir, fmt.Sprintf("realm-%d.json", r.ID))
	if err = writeAtomic(file, raw); err != nil {
		return nil, errors.New("cannot persist Realm configuration")
	}
	cmd := exec.Command(path, "-c", file)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	// Limit runtime threads on machines with many CPUs; each process serves one forwarding.
	cmd.Env = append(os.Environ(), "TOKIO_WORKER_THREADS=1")
	if err = cmd.Start(); err != nil {
		return nil, errors.New("cannot start Realm process")
	}
	p := &realmProcess{rule: r, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil, errors.New("Realm exited during startup; check binary and listener")
	case <-ctx.Done():
		p.stop()
		return nil, ctx.Err()
	case <-timer.C:
		return p, nil
	}
}

func (m *Manager) tableState(ctx context.Context) ([]byte, bool, error) {
	output, err := m.run(ctx, "nft", []string{"-j", "list", "tables"}, "")
	if err != nil {
		return nil, false, errors.New("cannot inspect nftables (requires nft and CAP_NET_ADMIN)")
	}
	var tables struct {
		Items []struct {
			Table *struct{ Family, Name string } `json:"table"`
		} `json:"nftables"`
	}
	if json.Unmarshal(output, &tables) != nil {
		return nil, false, errors.New("invalid nftables table response")
	}
	exists := false
	for _, item := range tables.Items {
		if item.Table != nil && item.Table.Family == "inet" && item.Table.Name == m.table {
			exists = true
		}
	}
	if !exists {
		return nil, false, nil
	}
	output, err = m.run(ctx, "nft", []string{"-j", "list", "table", "inet", m.table}, "")
	if err != nil {
		return nil, true, errors.New("cannot read forwarding table")
	}
	var state struct {
		Items []struct {
			Table *struct{ Comment string } `json:"table"`
		} `json:"nftables"`
	}
	if json.Unmarshal(output, &state) != nil {
		return nil, true, errors.New("invalid forwarding table")
	}
	owned := false
	for _, item := range state.Items {
		if item.Table != nil && item.Table.Comment == "yz-agent-portforward-v1:"+m.table {
			owned = true
		}
	}
	if !owned {
		// nft 1.0.6 omits table comments in JSON although the kernel stores them.
		text, textErr := m.run(ctx, "nft", []string{"list", "table", "inet", m.table}, "")
		if textErr == nil {
			owned = ownsTextTable(string(text), m.table)
		}
		if !owned {
			return nil, true, errors.New("forwarding table is not owned by this Agent; refusing to modify it")
		}
	}
	return output, true, nil
}
func (m *Manager) applyNFT(ctx context.Context, rules []resolvedRule) error {
	// Realm-only installations need no nft command or network capabilities. A marker
	// survives abrupt exits and makes stale NAT cleanup mandatory after restart.
	marker := filepath.Join(m.dir, "nft-owned")
	_, marked := os.Stat(marker)
	if len(rules) == 0 && m.nftHash == "" && os.IsNotExist(marked) {
		return nil
	}
	desired := fmt.Sprintf("%x", sha256.Sum256([]byte(nftScript(m.table, rules, false))))
	if len(rules) > 0 && strings.HasPrefix(m.nftHash, desired+":") && m.now().Sub(m.nftCheckedAt) < natCheckInterval {
		return nil
	}
	previous, previousErr := m.previousNFTRules()
	if previousErr != nil && len(rules) != 0 {
		return previousErr
	}
	state, exists, err := m.tableState(ctx)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		if exists {
			if _, err = m.run(ctx, "nft", []string{"delete", "table", "inet", m.table}, ""); err != nil {
				return errors.New("cannot remove managed forwarding table")
			}
		}
		if previousErr != nil {
			// Broken bookkeeping must not keep the listener open. Retain the
			// journal for recovery; the error reports incomplete flow cleanup.
			m.nftHash = ""
			return previousErr
		}
		if err = clearForwardingFlows(previous, nil); err != nil {
			return err
		}
		if err = m.saveNFTRules(nil); err != nil {
			return err
		}
		m.nftHash = ""
		m.nftCheckedAt = time.Time{}
		if err = os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	for _, r := range rules {
		path := "/proc/sys/net/ipv4/ip_forward"
		if r.IP.Is6() {
			path = "/proc/sys/net/ipv6/conf/all/forwarding"
		}
		value, e := os.ReadFile(path)
		if e != nil || strings.TrimSpace(string(value)) != "1" {
			return errors.New("nftables requires IP forwarding enabled for the target address family")
		}
	}
	script := nftScript(m.table, rules, exists)
	observed := fmt.Sprintf("%x", sha256.Sum256(state))
	if exists && m.nftHash == desired+":"+observed {
		if err := m.checkNFTConflicts(ctx, rules); err != nil {
			return err
		}
		m.nftCheckedAt = m.now()
		return nil
	}
	if err := m.checkNFTConflicts(ctx, rules); err != nil {
		return err
	}
	// Mark intent before creating rules, so crashes always leave a cleanup path.
	if err = writeAtomic(marker, []byte(m.table)); err != nil {
		return errors.New("cannot persist nftables ownership")
	}
	if _, err = m.run(ctx, "nft", []string{"-c", "-f", "-"}, script); err != nil {
		return errors.New("nftables forwarding validation failed")
	}
	journal := append([]resolvedRule{}, previous...)
	for _, rule := range rules {
		if !slices.Contains(journal, rule) {
			journal = append(journal, rule)
		}
	}
	if err = m.saveNFTRules(journal); err != nil {
		return err
	}
	if _, err = m.run(ctx, "nft", []string{"-f", "-"}, script); err != nil {
		return errors.New("nftables forwarding transaction failed")
	}
	if err = clearForwardingFlows(previous, rules); err != nil {
		return err
	}
	if err = m.saveNFTRules(rules); err != nil {
		return err
	}
	state, _, err = m.tableState(ctx)
	if err != nil {
		return err
	}
	m.nftHash = desired + ":" + fmt.Sprintf("%x", sha256.Sum256(state))
	m.nftCheckedAt = m.now()
	return nil
}
func nftScript(table string, rules []resolvedRule, exists bool) string {
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "delete table inet %s\n", table)
	}
	fmt.Fprintf(&b, "table inet %s {\n comment \"yz-agent-portforward-v1:%s\"\n chain prerouting { type nat hook prerouting priority dstnat; policy accept;\n", table, table)
	for _, r := range rules {
		family := "ip"
		target := r.IP.String()
		if r.IP.Is6() {
			family = "ip6"
			target = "[" + target + "]"
		}
		for _, proto := range strings.Split(r.Network, ",") {
			fmt.Fprintf(&b, " meta nfproto %s fib daddr type local %s dport %d dnat %s to %s:%d\n", map[string]string{"ip": "ipv4", "ip6": "ipv6"}[family], proto, r.ListenPort, family, target, r.Target.Port)
		}
	}
	b.WriteString(" }\n chain postrouting { type nat hook postrouting priority srcnat; policy accept;\n")
	for _, r := range rules {
		family := "ip"
		if r.IP.Is6() {
			family = "ip6"
		}
		for _, proto := range strings.Split(r.Network, ",") {
			fmt.Fprintf(&b, " %s daddr %s meta l4proto %s ct status dnat ct original proto-dst %d %s dport %d masquerade\n", family, r.IP, proto, r.ListenPort, proto, r.Target.Port)
		}
	}
	b.WriteString(" }\n}\n")
	return b.String()
}

func ownsTextTable(text, table string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "table inet "+table+" {" {
		return false
	}
	// Only a top-level declaration before the first child is proof of ownership.
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "{") || line == "}" {
			return false
		}
		if strings.HasPrefix(line, "comment ") {
			return line == "comment "+strconv.Quote("yz-agent-portforward-v1:"+table)
		}
	}
	return false
}
