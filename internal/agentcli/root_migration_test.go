package agentcli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigRootMigrationPreservesSettingsAndReverses(t *testing.T) {
	input := []byte(`# 保留用户注释
kernel:
  type: singbox
  config_dir: /etc/xboard-node/instances/fixture
  geo_data_dir: /etc/xboard-node/geo
  custom_config: /etc/xboard-node/custom.json
cert:
  cert_dir: /etc/xboard-node/certs
  cert_file: /external/cert.pem
  key_file: /etc/xboard-node-old/key.pem
firewall:
  state_dir: /etc/xboard-node/firewall
log:
  output: /etc/xboard-node/run.log
instances:
  - id: fixture
    kernel:
      config_dir: /etc/xboard-node/instances/fixture
    panel:
      token_env: FIXTURE_ENVIRONMENT
unknown: /etc/xboard-node/leave-this-value
`)
	updated, err := migrateConfigRoot(input, "/etc/xboard-node", "/etc/yz-agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"/etc/yz-agent/instances/fixture", "/etc/yz-agent/geo", "/etc/yz-agent/custom.json", "/etc/yz-agent/certs", "/etc/yz-agent/firewall", "/etc/yz-agent/run.log", "/external/cert.pem", "/etc/xboard-node-old/key.pem", "unknown: /etc/xboard-node/leave-this-value", "# 保留用户注释"} {
		if !strings.Contains(string(updated), value) {
			t.Fatalf("迁移后缺少预期设置: %s", value)
		}
	}
	repeated, err := migrateConfigRoot(updated, "/etc/xboard-node", "/etc/yz-agent")
	if err != nil || !bytes.Equal(updated, repeated) {
		t.Fatalf("重复迁移改变配置: %v", err)
	}
	restored, err := migrateConfigRoot(updated, "/etc/yz-agent", "/etc/xboard-node")
	if err != nil {
		t.Fatal(err)
	}
	var original, result map[string]any
	if err := yaml.Unmarshal(input, &original); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(restored, &result); err != nil {
		t.Fatal(err)
	}
	originalYAML, _ := yaml.Marshal(original)
	resultYAML, _ := yaml.Marshal(result)
	if !bytes.Equal(originalYAML, resultYAML) {
		t.Fatal("版本回退没有恢复原配置含义")
	}
}

func TestConfigRootMigrationPreservesSharedScalar(t *testing.T) {
	input := []byte("original: &fixture /etc/xboard-node/shared\nkernel:\n  config_dir: *fixture\n")
	updated, err := migrateConfigRoot(input, "/etc/xboard-node", "/etc/yz-agent")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Original string `yaml:"original"`
		Kernel   struct {
			ConfigDir string `yaml:"config_dir"`
		} `yaml:"kernel"`
	}
	if err := yaml.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if got.Original != "/etc/xboard-node/shared" || got.Kernel.ConfigDir != "/etc/yz-agent/shared" {
		t.Fatal("路径别名迁移影响了非路径设置")
	}
}

func TestConfigRootMigrationPreservesAliasUsersAndUnknownSections(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{
			name:  "路径锚点被其他字段引用",
			input: "kernel:\n  config_dir: &shared /etc/xboard-node/shared\noriginal: *shared\n",
			want:  "kernel:\n  config_dir: /etc/yz-agent/shared\noriginal: /etc/xboard-node/shared\n",
		},
		{
			name:  "配置段锚点被其他字段引用",
			input: "kernel: &shared\n  config_dir: /etc/xboard-node/shared\noriginal: *shared\n",
			want:  "kernel:\n  config_dir: /etc/yz-agent/shared\noriginal:\n  config_dir: /etc/xboard-node/shared\n",
		},
		{
			name:  "路径配置引用共享配置段",
			input: "original: &shared\n  config_dir: /etc/xboard-node/shared\nkernel: *shared\n",
			want:  "original:\n  config_dir: /etc/xboard-node/shared\nkernel:\n  config_dir: /etc/yz-agent/shared\n",
		},
		{
			name:  "路径配置合并共享配置段",
			input: "original: &shared\n  config_dir: /etc/xboard-node/shared\n  type: xray\nkernel:\n  <<: *shared\n  type: singbox\n",
			want:  "original:\n  config_dir: /etc/xboard-node/shared\n  type: xray\nkernel:\n  config_dir: /etc/yz-agent/shared\n  type: singbox\n",
		},
		{
			name:  "扩展字段中的同名配置保持原值",
			input: "kernel:\n  config_dir: /etc/xboard-node/shared\noriginal:\n  kernel:\n    config_dir: /etc/xboard-node/shared\n",
			want:  "kernel:\n  config_dir: /etc/yz-agent/shared\noriginal:\n  kernel:\n    config_dir: /etc/xboard-node/shared\n",
		},
		{
			name:  "根配置和实例的节点路径覆盖",
			input: "nodes:\n  - kernel:\n      config_dir: /etc/xboard-node/first\ninstances:\n  - nodes:\n      - cert:\n          cert_file: /etc/xboard-node/second.pem\n",
			want:  "nodes:\n  - kernel:\n      config_dir: /etc/yz-agent/first\ninstances:\n  - nodes:\n      - cert:\n          cert_file: /etc/yz-agent/second.pem\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := migrateConfigRoot([]byte(test.input), "/etc/xboard-node", "/etc/yz-agent")
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := yaml.Unmarshal(updated, &got); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("路径未迁移，或迁移改变了其他字段的含义")
			}
			repeated, err := migrateConfigRoot(updated, "/etc/xboard-node", "/etc/yz-agent")
			if err != nil || !bytes.Equal(updated, repeated) {
				t.Fatalf("重复迁移改变配置: %v", err)
			}
			restored, err := migrateConfigRoot(updated, "/etc/yz-agent", "/etc/xboard-node")
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(restored, &got); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal([]byte(test.input), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("回退未恢复原配置含义")
			}
		})
	}
}

func TestConfigRootMigrationWithoutChangedPathsKeepsOriginalBytes(t *testing.T) {
	input := []byte("original: &shared /external/shared\nkernel:\n  config_dir: *shared # 保留引用注释\n")
	got, err := migrateConfigRoot(input, "/etc/xboard-node", "/etc/yz-agent")
	if err != nil || !bytes.Equal(input, got) {
		t.Fatalf("无须迁移时改变了原文件: %v", err)
	}
}

func TestConfigRootMigrationFailureDoesNotOverwriteOutput(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "input.yml"), filepath.Join(dir, "output.yml")
	if err := os.WriteFile(target, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"kernel: [broken", "original: &cycle [*cycle]\n"} {
		if err := os.WriteFile(source, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runConfigMigrateRoot([]string{"--config", source, "--output", target, "--from", "/etc/xboard-node", "--to", "/etc/yz-agent"}); err == nil {
			t.Fatal("损坏或循环引用的配置必须拒绝迁移")
		}
		requireFileContents(t, target, "preserved")
	}
}
