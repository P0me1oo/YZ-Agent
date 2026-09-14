package installroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePreservesLegacyAndRejectsConflictingDirectories(t *testing.T) {
	dir := t.TempDir()
	current, legacy := filepath.Join(dir, "current"), filepath.Join(dir, "legacy")
	if got, err := resolve(current, legacy); err != nil || got != current {
		t.Fatalf("新安装目录错误: %s, %v", got, err)
	}
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(current, legacy); err != nil || got != legacy {
		t.Fatalf("旧安装目录丢失: %s, %v", got, err)
	}
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(current, legacy); err == nil {
		t.Fatal("两个独立安装目录必须先解决冲突")
	}
}

func TestCanonicalConfigKeepsOwnershipIdentity(t *testing.T) {
	for _, name := range []string{"config.yml", "instances/fixture/config.yml"} {
		legacy, current := filepath.Join(Legacy, name), filepath.Join(Current, name)
		if CanonicalConfig(current) != CanonicalConfig(legacy) {
			t.Fatal("目录迁移改变了防火墙实例标识")
		}
	}
	other := filepath.FromSlash("/etc/yz-agent-other/config.yml")
	if CanonicalConfig(other) != other {
		t.Fatal("不能合并其他配置目录的实例标识")
	}
}
