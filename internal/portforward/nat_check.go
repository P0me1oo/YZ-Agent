package portforward

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
)

var errNATInspectionUnavailable = errors.New("NAT inspection unavailable")

func (m *Manager) checkRealmNAT(ctx context.Context, p Policy, desired map[int]resolvedRule, statuses []Status) {
	rules := []resolvedRule{}
	for _, r := range p.Rules {
		if rr, ok := desired[r.ID]; ok && r.Mode == "realm" {
			rules = append(rules, rr)
		}
	}
	if len(rules) == 0 {
		return
	}
	raw, _ := json.Marshal(rules)
	fingerprint := sha256.Sum256(raw)
	if fingerprint != m.natPolicy || m.now().Sub(m.natCheckedAt) >= natCheckInterval {
		m.natState, m.natError = m.run(ctx, "nft", []string{"-j", "list", "ruleset"}, "")
		if m.natError != nil {
			m.natError = errNATInspectionUnavailable
		}
		m.natPolicy, m.natCheckedAt = fingerprint, m.now()
	}
	for i, r := range p.Rules {
		rr, ok := desired[r.ID]
		if !ok || r.Mode != "realm" {
			continue
		}
		if errors.Is(m.natError, errNATInspectionUnavailable) {
			// Realm needs no network administration privilege. Report exactly what
			// could not be checked without claiming the NAT allocation is verified.
			statuses[i].Warning = strings.TrimSpace(statuses[i].Warning + " 无法读取 NAT 规则，端口占用检查未完成")
			continue
		}
		if err := nftConflicts(m.natState, m.table, []resolvedRule{rr}); err != nil {
			statuses[i].State, statuses[i].Error = "failed", err.Error()
			delete(desired, r.ID)
		}
	}
}
