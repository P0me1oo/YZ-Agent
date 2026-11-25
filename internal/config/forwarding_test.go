package config

import (
	"context"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestForwardingLocalAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yaml  string
		valid bool
	}{
		{"default", "{}", true},
		{"explicit", "allowed_targets: [{cidr: 127.0.0.1/32, ports: [38141, 38142]}]", true},
		{"ipv6", "allowed_targets: [{cidr: 'fd00::/8', ports: [443]}]", true},
		{"missing-port", "allowed_targets: [{cidr: 127.0.0.1/32}]", false},
		{"invalid-port", "allowed_targets: [{cidr: 10.0.0.0/8, ports: [65536]}]", false},
		{"duplicate-port", "allowed_targets: [{cidr: 10.0.0.0/8, ports: [443, 443]}]", false},
		{"noncanonical", "allowed_targets: [{cidr: 10.1.1.1/8, ports: [443]}]", false},
		{"mapped", "allowed_targets: [{cidr: '::ffff:127.0.0.1/128', ports: [443]}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg ForwardingConfig
			if err := yaml.Unmarshal([]byte(tc.yaml), &cfg); err != nil {
				t.Fatal(err)
			}
			if err := ValidateForwarding(cfg); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestProtectedForwardingAuthorizationCanBeRevokedLocally(t *testing.T) {
	path, _, _ := privacyFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("forwarding:\n  allowed_targets:\n    - cidr: 127.0.0.1/32\n      ports: [443]\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	protected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plain map[string]any
	if err := yaml.Unmarshal(protected, &plain); err != nil {
		t.Fatal(err)
	}
	// Revoking the readable local policy must not resurrect the former grant
	// from the encrypted configuration payload.
	plain["forwarding"] = map[string]any{"allowed_targets": []any{}}
	encoded, err := yaml.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Forwarding.AllowedTargets) != 0 {
		t.Fatal("encrypted policy restored removed local authorization")
	}
}

func TestForwardingInheritanceCopiesLocalRulesAndHonorsEmptyOverride(t *testing.T) {
	parent := ForwardingConfig{AllowedTargets: []ForwardingTarget{{CIDR: "127.0.0.1/32", Ports: []int{443}}}}
	var inherited ForwardingConfig
	inherited.inheritFrom(parent)
	inherited.AllowedTargets[0].Ports[0] = 22
	if parent.AllowedTargets[0].Ports[0] != 443 {
		t.Fatal("inherited authorization aliases parent")
	}
	explicitEmpty := ForwardingConfig{AllowedTargets: []ForwardingTarget{}}
	explicitEmpty.inheritFrom(parent)
	if len(explicitEmpty.AllowedTargets) != 0 {
		t.Fatal("empty override inherited private access")
	}
	encoded, err := yaml.Marshal(Config{Forwarding: explicitEmpty})
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := yaml.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Forwarding.inheritFrom(parent)
	if restored.Forwarding.AllowedTargets == nil || len(restored.Forwarding.AllowedTargets) != 0 {
		t.Fatal("YAML rewrite erased explicit revocation")
	}
}
