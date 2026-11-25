package portforward

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Manager is owned by one orchestration loop. Processes are reaped by their done channel.
type Manager struct {
	dir, table   string
	realms       map[int]*realmProcess
	nftHash      string
	lock         *os.File
	lookup       lookupFunc
	run          commandFunc
	targets      []TargetPermission
	resolved     map[int]resolutionRecord
	now          func() time.Time
	nftCheckedAt time.Time
	natCheckedAt time.Time
	natPolicy    [32]byte
	natState     []byte
	natError     error
	closed       bool
}

func New(dir, identity string, targets ...TargetPermission) (*Manager, error) {
	if err := validateTargetPermissions(targets); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(identity))
	dir = filepath.Join(dir, fmt.Sprintf("forwarding-%x", sum[:8]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}
	return &Manager{dir: dir, table: fmt.Sprintf("yz_pf_%x", sum[:8]), realms: map[int]*realmProcess{}, lock: lock, lookup: resolve, run: runCommand, targets: cloneTargetPermissions(targets), resolved: map[int]resolutionRecord{}, now: time.Now}, nil
}
func (m *Manager) Load() (Policy, error) {
	var p Policy
	raw, err := os.ReadFile(filepath.Join(m.dir, "policy.json"))
	if err != nil {
		return p, err
	}
	if len(raw) > 4<<20 {
		return p, errors.New("cached policy too large")
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}
func (m *Manager) Save(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := filepath.Join(m.dir, "policy.json")
	if current, e := os.ReadFile(path); e == nil && string(current) == string(raw) {
		return nil
	}
	return writeAtomic(path, raw)
}
func writeAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".forwarding-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (m *Manager) Apply(ctx context.Context, p Policy) []Status {
	if err := p.Validate(); err != nil {
		return []Status{{ID: 0, State: "failed", Error: err.Error()}}
	}
	local := map[netip.Addr]bool{}
	addresses, _ := net.InterfaceAddrs()
	for _, a := range addresses {
		if prefix, e := netip.ParsePrefix(a.String()); e == nil {
			local[prefix.Addr().Unmap()] = true
		}
	}
	desired, statuses := m.resolveDesired(ctx, p, local)
	m.checkRealmNAT(ctx, p, desired, statuses)
	nftRules := []resolvedRule{}
	// Reap old or changed processes before reserving their ports for a new backend.
	for id, proc := range m.realms {
		rr, ok := desired[id]
		if !ok || rr.Mode != "realm" || rr != proc.rule || !proc.alive() {
			proc.stop()
			_ = os.Remove(filepath.Join(m.dir, fmt.Sprintf("realm-%d.json", id)))
			delete(m.realms, id)
		}
	}
	for i, r := range p.Rules {
		rr, ok := desired[r.ID]
		if !ok {
			continue
		}
		if r.Mode == "realm" && m.realms[r.ID] != nil {
			continue
		}
		err := checkPort(r)

		if err == nil && r.Mode == "nftables" {
			nftRules = append(nftRules, rr)
		}
		if err != nil {
			statuses[i].State, statuses[i].Error = "failed", err.Error()
		}
	}
	if err := m.applyNFT(ctx, nftRules); err != nil {
		// A failed transaction must not retain the older, more permissive NAT
		// policy. Cleanup removes only the table whose ownership was verified.
		if cleanupErr := m.applyNFT(ctx, nil); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("previous forwarding cleanup failed: %w", cleanupErr))
		}
		for i := range statuses {
			statuses[i].State, statuses[i].Error = "failed", err.Error()
		}
		// Failed cleanup must remain visible even after the last nftables rule is deleted.
		if len(p.Rules) == 0 {
			statuses = append(statuses, Status{ID: 0, State: "failed", Error: err.Error()})
		}
		return statuses
	}
	for i, rule := range p.Rules {
		if rule.Mode != "realm" || statuses[i].State != "running" || m.realms[rule.ID] != nil {
			continue
		}
		process, err := m.startRealm(ctx, desired[rule.ID])
		if err != nil {
			statuses[i].State, statuses[i].Error = "failed", err.Error()
		} else {
			m.realms[rule.ID] = process
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}
func (m *Manager) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	for id, p := range m.realms {
		p.stop()
		delete(m.realms, id)
	}
	// Cached policy is diagnostic only; every process requires fresh online authorization.
	err := m.applyNFT(context.Background(), nil)
	if m.lock != nil {
		_ = m.lock.Close()
	}
	return err
}
