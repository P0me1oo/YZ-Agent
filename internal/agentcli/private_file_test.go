package agentcli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWritePrivateFileCreateAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	for _, contents := range []string{"long initial settings", "short"} {
		if err := writePrivateFile(path, []byte(contents)); err != nil {
			t.Fatal(err)
		}
		requireFileContents(t, path, contents)
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("文件权限未收紧：%04o", info.Mode().Perm())
			}
		}
	}
}

func TestWritePrivateFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(dir, []byte("replacement")); err == nil {
		t.Fatal("不能将目录作为配置文件覆盖")
	}
	requireFileContents(t, path, "preserved")
}
