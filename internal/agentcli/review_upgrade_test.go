package agentcli

import (
	"errors"
	"testing"
)

func TestUpgradeRejectsMalformedTargetBeforeDownload(t *testing.T) {
	for _, version := range []string{"dev", "v01.13.0", "v2.40.1-beta.1"} {
		t.Run(version, func(t *testing.T) {
			paths, ops := upgradeFixture(t)
			ops.download = func(string, string) error {
				t.Fatal("目标版本无效时仍尝试下载")
				return nil
			}
			if _, err := upgradeBinaries(paths, version, ops); upgradeErrorCode(err) != "latest_version_invalid" {
				t.Fatalf("未在升级入口拒绝无效目标版本: %v", err)
			}
			requireNoUpgradeStage(t, paths)
		})
	}
}

func TestWrongReleaseVersionCannotReplaceOrMigrateInstallation(t *testing.T) {
	for _, output := range []string{"yz-agent v0.0.1", "xboard-node v0.0.1", "invalid", "yz-agent dev"} {
		t.Run(output, func(t *testing.T) {
			paths, ops := upgradeFixture(t)
			ops.run = func(string, ...string) ([]byte, error) { return []byte(output), nil }
			ops.migrate = func(string, string) error { t.Fatal("版本不匹配仍迁移"); return errors.New("unexpected") }
			ops.restart = func() error { t.Fatal("版本不匹配仍重启"); return nil }
			if _, err := upgradeBinaries(paths, "v2.29.0", ops); err == nil {
				t.Fatal("错版本升级报成功")
			}
			requireOriginalUpgradeFiles(t, paths)
		})
	}
	for _, actual := range []string{"xboard-node v1.13", "yz-agent 1.13.0", "agent v1.13.0"} {
		if err := verifyUpgradeVersion("v1.13.0", []byte(actual)); err != nil {
			t.Fatal("历史版本兼容被破坏", err)
		}
	}
}
