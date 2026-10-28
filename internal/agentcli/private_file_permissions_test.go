//go:build !windows

package agentcli

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestPrivateConfigWritersRestrictExistingPermissions(t *testing.T) {
	root := &config.RootConfig{Instances: []config.Config{{Panel: config.PanelConfig{
		URL: "https://privacy.example.invalid", NodeID: 1, NodeType: "vless", TokenEnv: "PRIVACY_TEST_KEEP",
	}}}}
	writers := []struct {
		name  string
		write func(string) error
	}{
		{"config", func(path string) error { return writeRootConfig(path, root) }},
		{"merge_credentials", func(path string) error {
			return mergeCredentials(path, path, "PRIVACY_TEST_NEW", rand.Text())
		}},
		{"prune_credentials", func(path string) error {
			return pruneCredentialKeys(path, []config.Config{{Panel: config.PanelConfig{TokenEnv: "PRIVACY_TEST_REMOVE"}}})
		}},
		{"install_metadata", func(path string) error { return writeInstallMetaVersioned(path, root, "test", "") }},
		{"migrated_config", func(path string) error {
			source := path + ".source"
			if err := os.WriteFile(source, []byte("kernel:\n  config_dir: /etc/xboard-node/instances/test\n"), 0o600); err != nil {
				return err
			}
			return runConfigMigrateRoot([]string{"--config", source, "--output", path, "--from", "/etc/xboard-node", "--to", "/etc/yz-agent"})
		}},
	}
	for _, writer := range writers {
		t.Run(writer.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private.conf")
			seed := []byte("PRIVACY_TEST_KEEP=" + rand.Text() + "\nPRIVACY_TEST_REMOVE=" + rand.Text() + "\n")
			if err := os.WriteFile(path, seed, 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := writer.write(path); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("保存后仍允许其他用户读取：权限为 %04o，期望 0600", info.Mode().Perm())
				}
			}
		})
	}
}

func TestWritePrivateFilePreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "config")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(link, []byte("updated")); err != nil {
		t.Fatal(err)
	}
	requireFileContents(t, target, "updated")
	linkInfo, err := os.Lstat(link)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("保存配置时不应替换原符号链接")
	}
	targetInfo, err := os.Stat(target)
	if err != nil || targetInfo.Mode().Perm() != 0o600 {
		t.Fatal("链接目标的配置权限未收紧")
	}
}
