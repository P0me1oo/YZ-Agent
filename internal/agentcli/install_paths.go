package agentcli

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/installroot"
)

const (
	defaultBinDir   = "/usr/local/bin"
	installLockName = ".install-lock"
)

var defaultBinDirFile = defaultInstallRoot + "/bin-dir"

type installPaths struct {
	binDir string
}

func (p installPaths) binary() string { return path.Join(p.binDir, "yz-agent") }
func (p installPaths) installedBinary() string {
	for _, name := range []string{serviceName, "yz-agent", "agent", "xboard-node"} {
		candidate := path.Join(p.binDir, name)
		if fileExists(candidate) {
			return candidate
		}
	}
	return p.binary()
}
func (p installPaths) cli() string {
	if path.Base(p.installedBinary()) == "xboard-node" {
		return path.Join(p.binDir, "xbctl")
	}
	return p.installedBinary()
}

// 程序目录会写入 shell 和 systemd 服务文件，只接受无需额外转义的绝对路径。
func validateBinDir(dir string) (string, error) {
	if !strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "//") {
		return "", errors.New("binary directory must be an absolute Linux path")
	}
	for _, c := range dir {
		if c != '/' && c != '.' && c != '_' && c != '-' &&
			(c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", errors.New("binary directory supports only letters, numbers, /, ., _, and -")
		}
	}
	clean := path.Clean(dir)
	if clean == "/" || clean != strings.TrimRight(dir, "/") {
		return "", errors.New("binary directory must not be / or contain repeated slashes, . or .. components")
	}
	return clean, nil
}

func loadInstallPaths(file string) (installPaths, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return installPaths{binDir: defaultBinDir}, nil
	}
	if err != nil {
		return installPaths{}, fmt.Errorf("read binary directory: %w", err)
	}
	dir, err := validateBinDir(strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return installPaths{}, fmt.Errorf("invalid %s: %w", file, err)
	}
	return installPaths{binDir: dir}, nil
}

func runConfigBinDir(args []string) error {
	file := defaultBinDirFile
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--path-file" {
			return errors.New("usage: yz-agent config bin-dir [--path-file PATH]")
		}
		file = args[1]
	}
	paths, err := loadInstallPaths(file)
	if err != nil {
		return err
	}
	fmt.Println(paths.binDir)
	return nil
}

// 与安装脚本共用锁目录，避免两个升级过程互相覆盖备份或安装位置。
func lockInstallation(root string) (func(), error) {
	roots := []string{root}
	if root == installroot.Current {
		roots = append(roots, installroot.Legacy)
	}
	if root == installroot.Legacy {
		roots = append(roots, installroot.Current)
	}
	return lockInstallationRoots(roots)
}

func lockInstallationRoots(roots []string) (func(), error) {
	root := roots[0]
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	lock := filepath.Join(root, installLockName)
	if err := os.Mkdir(lock, 0o700); err != nil {
		return nil, fmt.Errorf("cannot lock installation at %s; check for another installer or an interrupted operation: %w", lock, err)
	}
	info, err := os.Stat(lock)
	if err != nil {
		_ = os.Remove(lock)
		return nil, err
	}
	// Windows 的 Stat 延迟读取文件编号，必须在目录搬迁前固定身份。
	if !os.SameFile(info, info) {
		_ = os.Remove(lock)
		return nil, errors.New("cannot identify installation lock")
	}
	// 安装器可能在持锁期间迁移整个目录，只释放本次创建的同一个锁。
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, candidate := range roots {
				name := filepath.Join(candidate, installLockName)
				if current, err := os.Stat(name); err == nil && os.SameFile(info, current) {
					_ = os.Remove(name)
				}
			}
		})
	}, nil
}
