package agentcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeKernelFixture writes a two-instance config, optionally tagging the second
// instance with a node type.
func writeKernelFixture(t *testing.T, nodeType string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")

	second := ""
	if nodeType != "" {
		second = "\n        node_type: " + nodeType
	}

	content := `instances:
    - id: inst-a
      panel:
        url: https://panel.example.test
        token_env: A_KEY
        node_id: 7
      kernel:
        type: singbox
        log_level: warn
      log:
        level: info
        output: stdout
    - id: inst-b
      panel:
        url: https://panel.example.test
        token_env: B_KEY
        node_id: 9` + second + `
      kernel:
        type: singbox
        log_level: warn
      log:
        level: info
        output: stdout
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func kernelTypes(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var parsed struct {
		Instances []struct {
			ID    string `yaml:"id"`
			Panel struct {
				NodeID int `yaml:"node_id"`
			} `yaml:"panel"`
			Kernel struct {
				Type string `yaml:"type"`
			} `yaml:"kernel"`
		} `yaml:"instances"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	out := make(map[string]string, len(parsed.Instances))
	for _, inst := range parsed.Instances {
		if inst.ID == "" {
			t.Fatalf("instance id was dropped from %s", path)
		}
		out[inst.ID] = inst.Kernel.Type
	}
	return out
}

func TestConfigKernelSwitchesAllInstances(t *testing.T) {
	path := writeKernelFixture(t, "")

	if err := runConfigKernel([]string{"xray", "--config", path}); err != nil {
		t.Fatalf("switch to xray: %v", err)
	}

	got := kernelTypes(t, path)
	for id, kernel := range got {
		if kernel != "xray" {
			t.Fatalf("instance %s = %q, want xray", id, kernel)
		}
	}

	// Switching back must work too.
	if err := runConfigKernel([]string{"singbox", "--config", path}); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	for id, kernel := range kernelTypes(t, path) {
		if kernel != "singbox" {
			t.Fatalf("instance %s = %q, want singbox", id, kernel)
		}
	}
}

// Re-running the same switch must be a no-op rather than an error.
func TestConfigKernelIsIdempotent(t *testing.T) {
	path := writeKernelFixture(t, "")

	for i := 0; i < 3; i++ {
		if err := runConfigKernel([]string{"xray", "--config", path}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	for id, kernel := range kernelTypes(t, path) {
		if kernel != "xray" {
			t.Fatalf("instance %s = %q, want xray", id, kernel)
		}
	}
}

// --instance must leave every other instance untouched.
func TestConfigKernelTargetsSingleInstance(t *testing.T) {
	path := writeKernelFixture(t, "")

	if err := runConfigKernel([]string{"xray", "--instance", "inst-b", "--config", path}); err != nil {
		t.Fatalf("targeted switch: %v", err)
	}

	got := kernelTypes(t, path)
	if got["inst-a"] != "singbox" {
		t.Fatalf("inst-a = %q, want singbox (untouched)", got["inst-a"])
	}
	if got["inst-b"] != "xray" {
		t.Fatalf("inst-b = %q, want xray", got["inst-b"])
	}
}

func TestConfigKernelUnknownInstanceFails(t *testing.T) {
	path := writeKernelFixture(t, "")

	err := runConfigKernel([]string{"xray", "--instance", "nope", "--config", path})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a not-found error", err)
	}
	for id, kernel := range kernelTypes(t, path) {
		if kernel != "singbox" {
			t.Fatalf("instance %s changed to %q despite the failure", id, kernel)
		}
	}
}

// A node whose inbound xray cannot serve must not be switched silently.
func TestConfigKernelRefusesUnsupportedInbound(t *testing.T) {
	for _, nodeType := range []string{"tuic", "anytls", "mieru", "naive"} {
		t.Run(nodeType, func(t *testing.T) {
			path := writeKernelFixture(t, nodeType)

			err := runConfigKernel([]string{"xray", "--config", path})
			if err == nil || !strings.Contains(err.Error(), "xray kernel cannot host") {
				t.Fatalf("error = %v, want a refusal", err)
			}
			for id, kernel := range kernelTypes(t, path) {
				if kernel != "singbox" {
					t.Fatalf("instance %s was changed to %q despite the refusal", id, kernel)
				}
			}
		})
	}
}

func TestConfigKernelForceOverridesRefusal(t *testing.T) {
	path := writeKernelFixture(t, "tuic")

	if err := runConfigKernel([]string{"xray", "--force", "--config", path}); err != nil {
		t.Fatalf("forced switch: %v", err)
	}
	for id, kernel := range kernelTypes(t, path) {
		if kernel != "xray" {
			t.Fatalf("instance %s = %q, want xray", id, kernel)
		}
	}
}

// Switching to sing-box is always allowed, since it is the superset kernel.
func TestConfigKernelToSingboxAllowedForAnyNodeType(t *testing.T) {
	path := writeKernelFixture(t, "tuic")

	if err := runConfigKernel([]string{"xray", "--force", "--config", path}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runConfigKernel([]string{"singbox", "--config", path}); err != nil {
		t.Fatalf("switch to singbox: %v", err)
	}
	for id, kernel := range kernelTypes(t, path) {
		if kernel != "singbox" {
			t.Fatalf("instance %s = %q, want singbox", id, kernel)
		}
	}
}

func TestConfigKernelRejectsBadInput(t *testing.T) {
	path := writeKernelFixture(t, "")

	cases := map[string][]string{
		"unknown kernel": {"foo", "--config", path},
		"missing kernel": {"--config", path},
		"unknown flag":   {"xray", "--nope", "--config", path},
		"two kernels":    {"xray", "singbox", "--config", path},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if err := runConfigKernel(args); err == nil {
				t.Fatalf("expected an error for %v", args)
			}
		})
	}
}

// Xray remains the fallback when an instance has no explicit kernel type.
func TestConfigKernelTreatsEmptyTypeAsXray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	content := `instances:
    - id: inst-a
      panel:
        url: https://panel.example.test
        token_env: A_KEY
        node_id: 7
      kernel:
        log_level: warn
      log:
        level: info
        output: stdout
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := runConfigKernel([]string{"xray", "--config", path}); err != nil {
		t.Fatalf("switch: %v", err)
	}
	// Already effectively Xray, so the file keeps an empty type.
	if got := kernelTypes(t, path)["inst-a"]; got != "" {
		t.Fatalf("inst-a = %q, want empty type", got)
	}
	if err := runConfigKernel([]string{"singbox", "--config", path}); err != nil {
		t.Fatalf("switch to singbox: %v", err)
	}
	if got := kernelTypes(t, path)["inst-a"]; got != "singbox" {
		t.Fatalf("inst-a = %q, want singbox", got)
	}
}

func TestConfigKernelPreservesTimeSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	content := `time_sync:
  enabled: false
  servers:
    - ntp.example.com
  interval: 120
  timeout: 4
  warn_offset: 6
  error_offset: 16
  critical_offset: 26
instances:
  - id: inst-a
    panel:
      url: https://panel.example.test
      token_env: A_KEY
      node_id: 7
    kernel:
      type: singbox
      config_dir: /etc/yz-agent/inst-a
    log:
      level: info
      output: stdout
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := runConfigKernel([]string{"xray", "--config", path}); err != nil {
		t.Fatalf("switch kernel: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"time_sync:", "enabled: false", "ntp.example.com", "critical_offset: 26"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("rewritten config %q does not contain %q", string(data), want)
		}
	}
}
