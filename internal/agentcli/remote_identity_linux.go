package agentcli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func openRCManagedProcess(service string) bool {
	supervisor, err := exec.LookPath("supervise-daemon")
	if err != nil {
		return false
	}
	return openRCManagedProcessAt(
		service,
		"/run",
		"/proc",
		supervisor,
	)
}

func openRCManagedProcessAt(service, runDir, procDir, supervisor string) bool {
	pid, err := readOpenRCPID(filepath.Join(runDir, service+".pid"))
	if err != nil {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	parentPID := os.Getppid()
	if parentPID <= 1 || pid != parentPID {
		return false
	}
	// supervise-daemon 的 pidfile 保存守护进程编号，child_pid 才是实际 Agent。
	childPID, err := readOpenRCPID(filepath.Join(runDir, "openrc", "options", service, "child_pid"))
	if err != nil || childPID != os.Getpid() {
		return false
	}
	parentPath := filepath.Join(procDir, strconv.Itoa(parentPID))
	parentInfo, err := os.Stat(parentPath)
	if err != nil || !parentInfo.IsDir() || !openRCRecordOwned(parentInfo) {
		return false
	}
	supervisorInfo, err := os.Stat(supervisor)
	if err != nil || !supervisorInfo.Mode().IsRegular() {
		return false
	}
	if supervisorInfo.Mode().Perm()&0o022 != 0 || !openRCRecordOwned(supervisorInfo) {
		return false
	}
	parentExecutable, err := os.Stat(filepath.Join(parentPath, "exe"))
	if err != nil || !os.SameFile(parentExecutable, supervisorInfo) {
		return false
	}
	return os.Getppid() == parentPID
}

func readOpenRCPID(name string) (int, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	regular := info.Mode().IsRegular()
	protected := info.Mode().Perm()&0o022 == 0
	owned := openRCRecordOwned(info)
	if !regular || !protected || !owned {
		return 0, errors.New("untrusted OpenRC process record")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return 0, err
	}
	if len(data) > 64 {
		return 0, errors.New("oversized OpenRC process record")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, errors.New("invalid OpenRC process identifier")
	}
	return pid, nil
}

func openRCRecordOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
