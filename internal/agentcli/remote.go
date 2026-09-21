package agentcli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

var remoteMu sync.Mutex
var remoteIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type remoteRecord struct {
	Key       string                 `json:"key"`
	Operation panel.MachineOperation `json:"operation"`
}

func RemoteKey(panelURL string, machineID int) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", strings.TrimRight(panelURL, "/"), machineID))))
}

func remotePath(key string) string {
	return filepath.Join(defaultInstallRoot, "remote-operations", key+".json")
}

func RemoteAvailable() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	manager, err := detectServiceManager()
	if err != nil {
		return false
	}
	name := "setsid"
	if manager == serviceManagerSystemd {
		name = "systemd-run"
	}
	if _, err := exec.LookPath(name); err != nil {
		return false
	}
	// 容器或手工运行进程不冒充受服务管理器管理的安装。
	paths, err := loadInstallPaths(defaultBinDirFile)
	if err != nil {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	a, err := os.Stat(executable)
	if err != nil {
		return false
	}
	b, err := os.Stat(paths.installedBinary())
	if err != nil || !os.SameFile(a, b) {
		return false
	}
	servicePath := "/etc/init.d/yz-agent"
	if manager == serviceManagerSystemd {
		servicePath = "/etc/systemd/system/yz-agent.service"
	}
	return fileExists(servicePath)
}

func readRemote(key string) (*remoteRecord, error) {
	latest, err := readRemoteFile(remotePath(key))
	if err != nil {
		return nil, err
	}
	return readRemoteFile(remoteOperationPath(key, latest.Operation.ID))
}

func remoteOperationPath(key, id string) string {
	return filepath.Join(defaultInstallRoot, "remote-operations", key+"-"+id+".json")
}

func readRemoteFile(name string) (*remoteRecord, error) {
	body, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var record remoteRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func saveRemote(record *remoteRecord) error {
	return writeRemote(record, remoteOperationPath(record.Key, record.Operation.ID))
}

func activateRemote(record *remoteRecord) error {
	if err := saveRemote(record); err != nil {
		return err
	}
	return writeRemote(record, remotePath(record.Key))
}

func writeRemote(record *remoteRecord, name string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".operation-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

func RemoteResult(key string) *panel.MachineOperation {
	record, err := readRemote(key)
	if err != nil {
		return nil
	}
	result := record.Operation
	if result.Status == "running" && result.ExpiresAt <= time.Now().Unix() {
		result.Status, result.Error = "failed", "timeout"
	}
	return &result
}

// StartRemote 在落盘后才启动独立执行器；重试、配置重载和进程重启不会重复执行同一任务。
func StartRemote(key string, operation panel.MachineOperation) error {
	return startRemote(key, operation, RemoteAvailable, launchRemote)
}

func startRemote(key string, operation panel.MachineOperation, available func() bool, launch func(string, panel.MachineOperation) error) error {
	remoteMu.Lock()
	defer remoteMu.Unlock()
	if !remoteIDPattern.MatchString(operation.ID) || (operation.Action != "upgrade" && operation.Action != "restart") {
		return errors.New("invalid remote operation")
	}
	if previous, err := readRemoteFile(remoteOperationPath(key, operation.ID)); err == nil && previous.Operation.ID == operation.ID {
		return nil
	}
	if operation.ExpiresAt <= time.Now().Unix() || operation.ExpiresAt > time.Now().Add(16*time.Minute).Unix() {
		return errors.New("expired remote operation")
	}
	record := &remoteRecord{Key: key, Operation: operation}
	record.Operation.Status = "running"
	if !available() {
		record.Operation.Status, record.Operation.Error = "failed", "unsupported"
		return activateRemote(record)
	}
	if err := activateRemote(record); err != nil {
		return err
	}
	if err := launch(key, operation); err != nil {
		record.Operation.Status, record.Operation.Error = "failed", "launch_failed"
		return saveRemote(record)
	}
	return nil
}

func launchRemote(key string, operation panel.MachineOperation) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	manager, err := detectServiceManager()
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	if manager == serviceManagerSystemd {
		cmd = exec.Command("systemd-run", "--quiet", "--collect", "--unit=yz-agent-operation-"+operation.ID,
			executable, "internal-machine-operation", key, operation.ID)
	} else {
		cmd = exec.Command("setsid", executable, "internal-machine-operation", key, operation.ID)
	}
	// 不继承服务的日志管道；执行器只在本地记录固定结果码。
	if manager == serviceManagerSystemd {
		err = cmd.Run()
	} else {
		err = cmd.Start()
		if err == nil {
			go func() { _ = cmd.Wait() }()
		}
	}
	return err
}

type remoteExecution struct {
	lock             func() (func(), error)
	installationLock func() (func(), error)
	executable       func() (string, error)
	run              func(string, ...string) error
	upgrade          func() (string, error)
}

func runRemote(args []string) error {
	return runRemoteWith(args, remoteExecution{
		lock:             lockRemoteExecution,
		installationLock: func() (func(), error) { return lockInstallation(defaultInstallRoot) },
		executable:       os.Executable,
		run:              func(name string, args ...string) error { return exec.Command(name, args...).Run() },
		upgrade: func() (string, error) {
			var result string
			err := runUpgradeWithResult(nil, func(value string) { result = value })
			return result, err
		},
	})
}

func runRemoteWith(args []string, ops remoteExecution) error {
	if len(args) != 2 || len(args[0]) != 64 || strings.Trim(args[0], "0123456789abcdef") != "" || !remoteIDPattern.MatchString(args[1]) {
		return errors.New("invalid operation reference")
	}
	record, err := readRemote(args[0])
	if err != nil {
		return err
	}
	if record.Operation.ID != args[1] || record.Operation.Status != "running" {
		return errors.New("operation is no longer active")
	}
	finish := func(code string) error {
		// 每个任务写自己的结果文件，迟到结果不能覆盖新任务的活动指针。
		record.Operation.Status, record.Operation.Error = "succeeded", ""
		if code != "" {
			record.Operation.Status, record.Operation.Error = "failed", code
			record.Operation.Result = ""
		}
		return saveRemote(record)
	}
	unlock, err := ops.lock()
	if err != nil {
		return finish("busy")
	}
	defer unlock()
	// 取得全局执行锁后重读，防止两个执行器先后消费同一条记录。
	current, err := readRemote(args[0])
	if err != nil || current.Operation.ID != args[1] || current.Operation.Status != "running" {
		return errors.New("operation is no longer active")
	}
	if time.Now().Unix() >= record.Operation.ExpiresAt {
		return finish("timeout")
	}
	if record.Operation.Action == "upgrade" {
		// 执行器已脱离服务进程组，直接复用安装锁内的版本检查及安装事务。
		result, err := ops.upgrade()
		if err != nil {
			return finish(upgradeErrorCode(err))
		}
		if result != "updated" && result != "up_to_date" && result != "current_newer" {
			return finish("execution_failed")
		}
		record.Operation.Result = result
		return finish("")
	}
	executable, err := ops.executable()
	if err != nil {
		return finish("execution_failed")
	}
	commandArgs := []string{"service", "restart"}
	if record.Operation.Action != "restart" {
		return finish("unsupported")
	} else {
		// 重启也与本机手动升级互斥，避免打断已有安装事务。
		unlockInstallation, err := ops.installationLock()
		if err != nil {
			return finish("busy")
		}
		defer unlockInstallation()
	}
	// 独立执行器等待服务重启完成；面板超时不强杀子进程，仍持有执行锁。
	if err := ops.run(executable, commandArgs...); err != nil {
		return finish("execution_failed")
	}
	return finish("")
}

func latestStableRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadBase+"/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	target := filepath.Base(resp.Request.URL.Path)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("正式版查询返回 HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Request.URL.Path, "/tag/") || !releaseVersionPattern.MatchString(target) {
		return "", upgradeCheckFailure("latest_version_invalid", fmt.Errorf("无法识别最新正式版 %q，已停止升级", target))
	}
	return target, nil
}
