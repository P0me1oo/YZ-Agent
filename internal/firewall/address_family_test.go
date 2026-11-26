package firewall

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type familyCommands struct {
	commandSpy
	ipv6     bool
	probeErr error
	comments []string
}

func (c *familyCommands) IPv6Available() (bool, error)       { return c.ipv6, c.probeErr }
func (c *familyCommands) UFWIPv6Comments() ([]string, error) { return c.comments, nil }

func TestEffectivePlanUsesKernelCapabilityWithoutChangingDesiredPolicy(t *testing.T) {
	for _, kernel := range []string{"xray", "singbox"} {
		node := verifiedLanding()
		node.ListenIP = "0.0.0.0"
		original, err := PlanForNode(node, kernel)
		if err != nil {
			t.Fatal(err)
		}
		for _, available := range []bool{false, true} {
			backend := &systemBackend{commands: &familyCommands{ipv6: available}}
			got, err := backend.effectivePlan(original)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if available {
				want = 4
			}
			if len(got.Listeners) != want || len(original.Listeners) != 4 || !got.RelayRestricted {
				t.Fatalf("wrong effective policy: %+v", got)
			}
			if blockedBy(got, "192.0.2.1") || !blockedBy(got, "192.0.2.3") {
				t.Fatal("IPv4 protection changed")
			}
		}
	}
}

func TestEffectivePlanRejectsUnknownCapabilityAndExplicitIPv6(t *testing.T) {
	backend := &systemBackend{commands: &familyCommands{probeErr: errors.New("permission denied")}}
	plan, _ := PlanForNode(verifiedLanding(), "singbox")
	if _, err := backend.effectivePlan(plan); err == nil {
		t.Fatal("uncertainty silently disabled IPv6")
	}
	node := verifiedLanding()
	node.ListenIP = "2001:db8::1"
	plan, _ = PlanForNode(node, "singbox")
	backend.commands = &familyCommands{}
	if _, err := backend.effectivePlan(plan); err == nil {
		t.Fatal("IPv6-only listener was silently removed")
	}
}

func TestDisabledIPv6OnlyForgetsAbsentUFWIntent(t *testing.T) {
	runner := &familyCommands{}
	backend, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "fixture", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backend.ipv6Unavailable = true
	owned := ownedRule{Backend: "ufw", Rule: Rule{Family: 6, Protocol: "tcp", Ports: portset.Range{From: 443, To: 443}}}
	if err := backend.remember(owned); err != nil {
		t.Fatal(err)
	}
	runner.comments = []string{backend.ufwComment(owned.Rule)}
	if err := backend.removeUFW(context.Background(), owned, nil, ""); err == nil || len(backend.state.Owned) != 1 {
		t.Fatal("persistent rule ownership was lost")
	}
	runner.comments = []string{"manual unrelated rule"}
	if err := backend.removeUFW(context.Background(), owned, nil, ""); err != nil || len(backend.state.Owned) != 0 || len(runner.calls) != 0 {
		t.Fatalf("absent failed intent was not removed safely: %v", err)
	}
}

func TestUFWWrittenCommentEncodingIsVerified(t *testing.T) {
	marker := "yz-agent:node=1:owner:123456"
	comments, err := ufwPersistentComments("### tuple ### allow tcp 443 ::/0 any ::/0 in comment=" + hex.EncodeToString([]byte(marker)))
	if err != nil || len(comments) != 1 || comments[0] != marker {
		t.Fatal(comments, err)
	}
	if _, err := ufwPersistentComments("### tuple ### allow comment=broken"); err == nil {
		t.Fatal("invalid encoding accepted")
	}
}

type ipv4UFW struct{ sourceUFW }

func (*ipv4UFW) IPv6Available() (bool, error)       { return false, nil }
func (*ipv4UFW) UFWIPv6Comments() ([]string, error) { return nil, nil }
func (r *ipv4UFW) Run(ctx context.Context, name, input string, args ...string) (string, error) {
	if strings.Join(args, " ") == "show added" {
		return "", nil
	}
	for _, arg := range args {
		if arg == "::/0" {
			return "ERROR: IPv6 support not enabled", errors.New("unexpected IPv6 mutation")
		}
	}
	return r.sourceUFW.Run(ctx, name, input, args...)
}

func TestIPv4UFWReconcileRecoversFailedIPv6IntentAndStaysIdempotent(t *testing.T) {
	runner := &ipv4UFW{sourceUFW: sourceUFW{rules: map[Rule]string{}}}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "recovery", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	node := verifiedLanding()
	node.ListenIP = "0.0.0.0"
	plan, err := PlanForNode(node, "singbox")
	if err != nil {
		t.Fatal(err)
	}
	plan, err = b.effectivePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	b.plans = map[string]Plan{"panel/node/1": plan}
	failed := ownedRule{Backend: "ufw", Rule: Rule{Family: 6, Protocol: "tcp", Ports: portset.Range{From: 28388, To: 28388}}, Priority: -1}
	if err := b.remember(failed); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := b.Apply(t.Context(), plan.Listeners, nil); err != nil {
			t.Fatal(err)
		}
		if len(runner.rules) != 2 || len(b.state.Owned) != 2 {
			t.Fatal("unexpected rules after recovery")
		}
	}
	if runner.writes != 2 {
		t.Fatalf("repeated reconciliation wrote rules: %d", runner.writes)
	}
}

func TestCommandFailureReportsOnlyFixedIPv6Cause(t *testing.T) {
	cause := errors.New("exit status 1")
	err := commandFailure("ufw", "private-admin-comment\nERROR: IPv6 support not enabled", cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "IPv6 支持未启用") || strings.Contains(err.Error(), "private-admin") {
		t.Fatal(err)
	}
}
