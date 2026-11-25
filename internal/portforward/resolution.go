package portforward

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	resolutionRefresh = 15 * time.Second
	resolutionGrace   = 60 * time.Second
	natCheckInterval  = 30 * time.Second
)

var errTemporaryResolution = errors.New("temporary forwarding DNS failure")

type resolutionRecord struct {
	rule      resolvedRule
	policy    [32]byte
	checkedAt time.Time
}

func resolutionError(message string, err error) error {
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return fmt.Errorf("%s: %w", message, errTemporaryResolution)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (m *Manager) resolveDesired(
	ctx context.Context,
	p Policy,
	local map[netip.Addr]bool,
) (map[int]resolvedRule, []Status) {
	raw, _ := json.Marshal(p)
	fingerprint := sha256.Sum256(raw)
	now := m.now()
	cache := map[string][]netip.Addr{}
	lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
		if ips, ok := cache[host]; ok {
			return ips, nil
		}
		ips, err := m.lookup(ctx, host)
		if err == nil {
			cache[host] = ips
		}
		return ips, err
	}
	desired := map[int]resolvedRule{}
	statuses := make([]Status, 0, len(p.Rules))
	next := map[int]resolutionRecord{}
	for _, r := range p.Rules {
		state := Status{ID: r.ID, State: "running"}
		previous, known := m.resolved[r.ID]
		unchanged := known && previous.policy == fingerprint && previous.rule.Rule == r
		if unchanged && m.allowsTarget(previous.rule.IP, r.Target.Port, local) && now.Sub(previous.checkedAt) < resolutionRefresh {
			desired[r.ID], next[r.ID] = previous.rule, previous
			statuses = append(statuses, state)
			continue
		}
		checkedLookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			ips, err := lookup(ctx, host)
			if err == nil && host == r.Target.Address {
				for _, ip := range ips {
					if !m.allowsTarget(ip, r.Target.Port, local) {
						return nil, errors.New("forwarding target requires local authorization")
					}
				}
			}
			return ips, err
		}
		rr, err := resolveRule(ctx, r, p, local, checkedLookup)
		if err == nil {
			desired[r.ID] = rr
			next[r.ID] = resolutionRecord{rule: rr, policy: fingerprint, checkedAt: now}
		} else if unchanged && errors.Is(err, errTemporaryResolution) && ctx.Err() == nil && now.Sub(previous.checkedAt) < resolutionGrace && m.allowsTarget(previous.rule.IP, r.Target.Port, local) {
			// Keep only a previously verified numeric destination. Never extend its
			// deadline on failure, or reuse it for a changed topology.
			desired[r.ID], next[r.ID] = previous.rule, previous
			state.Warning = "DNS 暂时不可用，短期使用此前已核验的目标地址"
		} else {
			state.State, state.Error = "failed", err.Error()
		}
		statuses = append(statuses, state)
	}
	m.resolved = next
	return desired, statuses
}
