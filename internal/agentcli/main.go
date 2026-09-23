package agentcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/buildinfo"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/installroot"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
	"gopkg.in/yaml.v3"
)

var (
	defaultInstallRoot     = installroot.Path()
	defaultConfigPath      = defaultInstallRoot + "/config.yml"
	defaultMetaPath        = defaultInstallRoot + "/install-meta.json"
	defaultCredentialsPath = defaultInstallRoot + "/credentials.env"
	downloadBase           = "https://github.com/P0me1oo/YZ-Agent/releases"
)

var (
	version   = "v1.17.3"
	buildTime = "unknown"
	commit    = "unknown"
)

type instanceRow struct {
	ID      string `json:"id"`
	Mode    string `json:"mode"`
	Panel   string `json:"panel"`
	Target  string `json:"target"`
	Service string `json:"service"`
	Health  string `json:"health"`
}

type fileRootConfig struct {
	Log       *fileLogConfig         `yaml:"log,omitempty"`
	Kernel    *fileKernelConfig      `yaml:"kernel,omitempty"`
	Node      *fileNodeConfig        `yaml:"node,omitempty"`
	WS        *config.WSConfig       `yaml:"ws,omitempty"`
	Runtime   *fileRuntimeConfig     `yaml:"runtime,omitempty"`
	Cert      *config.CertConfig     `yaml:"cert,omitempty"`
	TimeSync  *config.TimeSyncConfig `yaml:"time_sync,omitempty"`
	Firewall  *config.FirewallConfig `yaml:"firewall,omitempty"`
	Instances []fileInstance         `yaml:"instances,omitempty"`
}

type fileInstance struct {
	ID         string                 `yaml:"id,omitempty"`
	Panel      filePanelConfig        `yaml:"panel"`
	Node       *fileNodeConfig        `yaml:"node,omitempty"`
	Kernel     fileKernelConfig       `yaml:"kernel"`
	Log        fileLogConfig          `yaml:"log"`
	Runtime    *fileRuntimeConfig     `yaml:"runtime,omitempty"`
	HealthPort int                    `yaml:"health_port,omitempty"`
	Machine    *fileMachineConfig     `yaml:"machine,omitempty"`
	Standalone map[string]any         `yaml:"standalone,omitempty"`
	Cert       *config.CertConfig     `yaml:"cert,omitempty"`
	WS         *config.WSConfig       `yaml:"ws,omitempty"`
	TimeSync   *config.TimeSyncConfig `yaml:"time_sync,omitempty"`
	Firewall   *config.FirewallConfig `yaml:"firewall,omitempty"`
	Nodes      []config.NodeEntry     `yaml:"nodes,omitempty"`
}

type filePanelConfig struct {
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env,omitempty"`
	NodeID   int    `yaml:"node_id,omitempty"`
	NodeType string `yaml:"node_type,omitempty"`
}

type fileMachineConfig struct {
	MachineID int    `yaml:"machine_id"`
	TokenEnv  string `yaml:"token_env,omitempty"`
}

type fileNodeConfig struct {
	PushInterval         int `yaml:"push_interval,omitempty"`
	PullInterval         int `yaml:"pull_interval,omitempty"`
	TrackInterval        int `yaml:"track_interval,omitempty"`
	DeviceReportInterval int `yaml:"device_report_interval,omitempty"`
}

type fileKernelConfig struct {
	Type                string           `yaml:"type"`
	ConfigDir           string           `yaml:"config_dir"`
	LogLevel            string           `yaml:"log_level,omitempty"`
	GeoDataDir          string           `yaml:"geo_data_dir,omitempty"`
	CustomConfig        string           `yaml:"custom_config,omitempty"`
	CustomRoute         []map[string]any `yaml:"custom_route,omitempty"`
	CustomOut           []map[string]any `yaml:"custom_outbound,omitempty"`
	RealityMinClientVer string           `yaml:"reality_min_client_ver,omitempty"`
}

type fileLogConfig struct {
	Level  string `yaml:"level,omitempty"`
	Output string `yaml:"output,omitempty"`
}

type fileRuntimeConfig struct {
	GoMemLimit  string `yaml:"gomemlimit,omitempty"`
	GoGCPercent int    `yaml:"gogc,omitempty"`
}

type instanceSummary struct {
	ID         string `json:"id"`
	PanelURL   string `json:"panel_url"`
	Mode       string `json:"mode"`
	NodeID     *int   `json:"node_id"`
	MachineID  *int   `json:"machine_id"`
	HealthPort int    `json:"health_port"`
}

type installMeta struct {
	ConfigMode       string            `json:"config_mode"`
	Version          string            `json:"version"`
	LatestInstanceID string            `json:"latest_instance_id"`
	InstanceCount    int               `json:"instance_count"`
	Instances        []instanceSummary `json:"instances"`
	UpdatedAt        string            `json:"updated_at"`
}

func loadInstallMeta(path string) (*installMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := &installMeta{}
	if err := json.Unmarshal(data, meta); err != nil {
		return nil, fmt.Errorf("parse install meta: %w", err)
	}
	return meta, nil
}

// Run 执行管理子命令，由统一入口传入构建版本。
func Run(args []string, buildVersion, builtAt, revision string) error {
	version, buildTime, commit = buildVersion, builtAt, revision
	selectInstalledService()
	return run(args)
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "internal-machine-operation":
		return runRemote(args[1:])
	case "status":
		return runStatus()
	case "list":
		return runList(args[1:])
	case "instance":
		return runInstance(args[1:])
	case "service":
		return runService(args[1:])
	case "logs", "log":
		return runService([]string{"logs"})
	case "health":
		return runHealth()
	case "doctor":
		return runDoctor(args[1:])
	case "bind":
		return runBind(args[1:])
	case "bind-node":
		return runBind(append([]string{"add-node"}, args[1:]...))
	case "bind-machine":
		return runBind(append([]string{"add-machine"}, args[1:]...))
	case "unbind-node":
		return runBind(append([]string{"remove-node"}, args[1:]...))
	case "unbind-machine":
		return runBind(append([]string{"remove-machine"}, args[1:]...))
	case "start", "stop", "restart", "enable", "disable":
		return runService(args)
	case "upgrade":
		return runUpgrade(args[1:])
	case "uninstall":
		return runUninstall(args[1:])
	case "version", "-v", "--version":
		fmt.Println(buildinfo.Report("yz-agent", version, buildTime, commit))
		return nil
	case "config":
		return runConfig(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage() {
	fmt.Println(`yz-agent commands:
  yz-agent help
  yz-agent run [-c PATH]
  yz-agent status
  yz-agent list [--output text|json]
  yz-agent instance list [--output text|json]
  yz-agent instance get <id> [--output text|json]
  yz-agent config init --mode node|machine --panel-url URL --token TOKEN [flags]
  yz-agent config health-port [--config PATH]
  yz-agent config bin-dir [--path-file PATH]
  yz-agent config kernel <xray|singbox> [--instance ID] [--config PATH] [--force]
  yz-agent service status|start|stop|restart|enable|disable|logs
  yz-agent health
  yz-agent doctor time [--config PATH] [--output text|json]
  yz-agent bind add-node --panel-url URL --token TOKEN --node-id ID [--node-type TYPE] [--kernel singbox|xray]
  yz-agent bind add-machine --panel-url URL --token TOKEN --machine-id ID [--kernel singbox|xray]
  yz-agent bind remove <instance-id>
  yz-agent bind remove-node --panel URL --node-id ID
  yz-agent bind remove-machine --panel URL --machine-id ID
  yz-agent upgrade [--version VERSION]
  yz-agent uninstall [--purge] [--yes]
  yz-agent version

shortcuts:
  yz-agent start|stop|restart        = yz-agent service start|stop|restart
  yz-agent log|logs                  = yz-agent service logs
  yz-agent bind-node ...             = yz-agent bind add-node ...
  yz-agent bind-machine ...          = yz-agent bind add-machine ...
  yz-agent unbind-node ...           = yz-agent bind remove-node ...
  yz-agent unbind-machine ...        = yz-agent bind remove-machine ...`)
}

func runStatus() error {
	fmt.Println("yz-agent status")
	fmt.Println()

	// Version from install-meta.json
	ver := "unknown"
	if meta, err := loadInstallMeta(defaultMetaPath); err == nil {
		ver = meta.Version
	}
	fmt.Printf("  version:  %s\n", ver)
	if paths, err := loadInstallPaths(defaultBinDirFile); err == nil {
		fmt.Printf("  bin-dir:  %s\n", paths.binDir)
	}

	// Service status
	svc := serviceState()
	fmt.Printf("  service:  %s\n", svc)

	// Health
	health := instanceAwareHealth()
	fmt.Printf("  health:   %s\n", health)
	fmt.Println()

	// Instance list
	rows, err := collectInstanceRows()
	if err != nil {
		fmt.Printf("  (no instances found: %v)\n", err)
		return nil
	}
	return printRows(rows, "text")
}

func runList(args []string) error {
	output := parseOutput(args)
	rows, err := collectInstanceRows()
	if err != nil {
		return err
	}
	return printRows(rows, output)
}

func runInstance(args []string) error {
	if len(args) == 0 {
		return runList(nil)
	}
	if args[0] == "list" {
		return runList(args[1:])
	}
	if args[0] != "get" {
		return fmt.Errorf("unknown instance command: %s", args[0])
	}
	if len(args) < 2 {
		return errors.New("usage: yz-agent instance get <id> [--output text|json]")
	}
	id := args[1]
	output := parseOutput(args[2:])
	rows, err := collectInstanceRows()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == id {
			return printRows([]instanceRow{row}, output)
		}
	}
	return fmt.Errorf("instance not found: %s", id)
}

func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yz-agent service <status|start|stop|restart|enable|disable|logs>")
	}
	return runDetectedManagedService(args[0], os.Geteuid() != 0, args[1:]...)
}

func runHealth() error {
	h := instanceAwareHealth()
	fmt.Println(h)
	if h == "down" {
		return errors.New("health check failed")
	}
	return nil
}

type timeDoctorResult struct {
	ConfiguredEnabled bool              `json:"configured_enabled"`
	Servers           []string          `json:"servers"`
	Probe             timesync.Snapshot `json:"probe"`
}

func runDoctor(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yz-agent doctor time [--config PATH] [--output text|json]")
	}
	switch args[0] {
	case "time":
		return runDoctorTime(args[1:])
	default:
		return fmt.Errorf("unknown doctor command: %s", args[0])
	}
}

func runDoctorTime(args []string) error {
	configPath := defaultConfigPath
	output := "text"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				return errors.New("--config requires a path")
			}
			i++
			configPath = args[i]
		case "--output":
			if i+1 >= len(args) {
				return errors.New("--output requires text or json")
			}
			i++
			output = strings.ToLower(strings.TrimSpace(args[i]))
			if output != "text" && output != "json" {
				return fmt.Errorf("output must be text or json, got %q", output)
			}
		default:
			return fmt.Errorf("unknown doctor time flag: %s", args[i])
		}
	}

	timeConfig, configuredEnabled, err := loadDoctorTimeConfig(configPath)
	if err != nil {
		return err
	}
	// doctor 始终执行只读探测，即使运行时自动校准被显式关闭。
	probeEnabled := true
	timeConfig.Enabled = &probeEnabled
	manager := timesync.New(timeConfig)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeConfig.Timeout+1)*time.Second)
	defer cancel()
	snapshot, probeErr := manager.Check(ctx)
	result := timeDoctorResult{
		ConfiguredEnabled: configuredEnabled,
		Servers:           append([]string(nil), timeConfig.Servers...),
		Probe:             snapshot,
	}
	if err := printTimeDoctor(os.Stdout, output, result); err != nil {
		return err
	}
	if probeErr != nil {
		return fmt.Errorf("time check failed: %w", probeErr)
	}
	if snapshot.Status != timesync.StatusNormal {
		return fmt.Errorf("time check status is %s (offset %d ms)", snapshot.Status, snapshot.OffsetMS)
	}
	return nil
}

func loadDoctorTimeConfig(path string) (config.TimeSyncConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.TimeSyncConfig{}, false, fmt.Errorf("read config: %w", err)
	}
	root := &config.RootConfig{}
	if err := yaml.Unmarshal(data, root); err != nil {
		return config.TimeSyncConfig{}, false, fmt.Errorf("parse config: %w", err)
	}
	if len(root.Instances) == 0 {
		resolved := config.ResolveTimeSync(root.Config.TimeSync, config.TimeSyncConfig{})
		if err := config.ValidateTimeSync(resolved); err != nil {
			return config.TimeSyncConfig{}, false, err
		}
		return resolved, resolved.IsEnabled(), nil
	}
	resolved := config.ResolveTimeSync(root.Instances[0].TimeSync, root.Config.TimeSync)
	if err := config.ValidateTimeSync(resolved); err != nil {
		return config.TimeSyncConfig{}, false, err
	}
	for i := 1; i < len(root.Instances); i++ {
		other := config.ResolveTimeSync(root.Instances[i].TimeSync, root.Config.TimeSync)
		if err := config.ValidateTimeSync(other); err != nil {
			return config.TimeSyncConfig{}, false, fmt.Errorf("instances[%d]: %w", i, err)
		}
		if !resolved.Equal(other) {
			return config.TimeSyncConfig{}, false, fmt.Errorf("time_sync differs between instances[0] and instances[%d]", i)
		}
	}
	return resolved, resolved.IsEnabled(), nil
}

func printTimeDoctor(w io.Writer, output string, result timeDoctorResult) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	configured := "enabled"
	if !result.ConfiguredEnabled {
		configured = "disabled"
	}
	fmt.Fprintln(w, "time sync doctor")
	fmt.Fprintf(w, "  configured: %s\n", configured)
	fmt.Fprintf(w, "  status:     %s\n", result.Probe.Status)
	fmt.Fprintf(w, "  offset:     %d ms\n", result.Probe.OffsetMS)
	if result.Probe.Source != "" {
		fmt.Fprintf(w, "  source:     %s\n", result.Probe.Source)
	}
	if result.Probe.LastSuccess != nil {
		fmt.Fprintf(w, "  checked_at: %s\n", result.Probe.LastSuccess.UTC().Format(time.RFC3339))
	}
	if result.Probe.LastError != "" {
		fmt.Fprintf(w, "  error:      %s\n", result.Probe.LastError)
	}
	fmt.Fprintf(w, "  servers:    %s\n", strings.Join(result.Servers, ", "))
	return nil
}

func runBind(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yz-agent bind <add-node|add-machine|remove-node|remove-machine> ...")
	}
	if err := ensureRoot("bind"); err != nil {
		return err
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add-node":
		return runBindAdd("node", rest)
	case "add-machine":
		return runBindAdd("machine", rest)
	case "remove-node":
		panel, nodeID, err := parseRemoveNodeArgs(rest)
		if err != nil {
			return err
		}
		return removeBinding(panel, nodeID, 0, "")
	case "remove-machine":
		panel, machineID, err := parseRemoveMachineArgs(rest)
		if err != nil {
			return err
		}
		return removeBinding(panel, 0, machineID, "")
	case "remove":
		if len(rest) == 0 {
			return errors.New("usage: yz-agent bind remove <instance-id>")
		}
		return removeBinding("", 0, 0, rest[0])
	default:
		return fmt.Errorf("unknown bind command: %s", sub)
	}
}

func runBindAdd(mode string, args []string) error {
	// Build configInit args from bind args
	initArgs := []string{
		"--mode", mode,
		"--config", defaultConfigPath,
		"--output", defaultConfigPath,
		"--credentials-in", defaultCredentialsPath,
		"--credentials-out", defaultCredentialsPath,
		"--meta", defaultMetaPath,
		"--install-root", defaultInstallRoot,
	}
	// Pass through remaining args (--panel-url, --token, --node-id, --machine-id, --kernel, etc.)
	initArgs = append(initArgs, args...)

	if err := runConfigInit(initArgs); err != nil {
		return fmt.Errorf("bind failed: %w", err)
	}
	// Restart service to pick up new config
	fmt.Println("Restarting service...")
	if err := runDetectedManagedService("restart", false); err != nil {
		return fmt.Errorf("service restart failed: %w", err)
	}
	fmt.Println("Binding added successfully")
	return nil
}

func runUpgrade(args []string) error {
	return runUpgradeWithResult(args, func(string) {})
}

func runUpgradeWithResult(args []string, result func(string)) error {
	if err := ensureRoot("upgrade"); err != nil {
		return err
	}
	release := "latest"
	for i := 0; i < len(args); i++ {
		if args[i] != "--version" || i+1 >= len(args) || args[i+1] == "" {
			return errors.New("usage: yz-agent upgrade [--version VERSION]; change the binary directory with install.sh upgrade --bin-dir PATH")
		}
		i++
		release = args[i]
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
	if _, err := installroot.Resolve(); err != nil {
		return err
	}
	unlock, err := lockInstallation(defaultInstallRoot)
	if err != nil {
		return err
	}
	defer unlock()
	paths, err := loadInstallPaths(defaultBinDirFile)
	if err != nil {
		return err
	}
	if release == "latest" {
		report, err := exec.Command(paths.installedBinary(), "-v").Output()
		if err != nil {
			return upgradeCheckFailure("current_version_failed", fmt.Errorf("读取当前版本失败，已停止升级：%w", err))
		}
		var message string
		release, message, err = planLatestUpgrade(context.Background(), installedVersion("", report), latestStableRelease)
		if err != nil {
			return err
		}
		if message != "" {
			if message == "up_to_date" {
				fmt.Println("已是最新版本")
			} else {
				fmt.Println("当前版本高于最新正式版，不自动降级")
			}
			result(message)
			return nil
		}
	}
	manager, err := detectServiceManager()
	if err != nil {
		return err
	}

	fmt.Printf("Starting upgrade in %s...\n", paths.binDir)
	if err := validateInstalledServices(manager); err != nil {
		return err
	}
	newVer, err := upgradeBinaries(paths, release, upgradeOperations{
		download: func(url, destination string) error {
			fmt.Printf("Downloading %s...\n", url)
			return downloadFile(url, destination)
		},
		run: func(file string, args ...string) ([]byte, error) {
			return exec.Command(file, args...).CombinedOutput()
		},
		restart: func() error {
			fmt.Println("Restarting service...")
			if err := reloadServiceManager(manager); err != nil {
				return err
			}
			return runManagedService(manager, "restart", false)
		},
		rename: os.Rename,
		migrate: func(node, cli string) error {
			return migrateInstallation(paths, release, node, cli)
		},
	})
	if err != nil {
		return err
	}

	if root, err := loadWritableRootConfig(defaultConfigPath); err == nil {
		instances, _ := root.NormalizeInstances()
		if err := writeInstallMetaVersioned(defaultMetaPath, root, newVer, latestInstanceID(instances)); err != nil {
			fmt.Printf("Warning: update install metadata failed: %v\n", err)
		}
	}
	fmt.Printf("Upgrade complete (version: %s)\n", newVer)
	result("updated")
	return nil
}
func runUninstall(args []string) error {
	if err := ensureRoot("uninstall"); err != nil {
		return err
	}
	if _, err := installroot.Resolve(); err != nil {
		return err
	}

	purge := false
	yes := false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "--yes", "-y":
			yes = true
		}
	}

	if !yes {
		fmt.Print("Proceed with uninstall? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Uninstall cancelled")
			return nil
		}
	}

	var warnings []string
	unlock, err := lockInstallation(defaultInstallRoot)
	if err != nil {
		return err
	}
	defer unlock()
	paths, err := loadInstallPaths(defaultBinDirFile)
	if err != nil {
		return err
	}

	manager, managerErr := detectServiceManager()
	if managerErr != nil {
		return managerErr
	}
	if err := validateInstalledServices(manager); err != nil {
		return err
	}
	serviceFilePath, pathErr := serviceFilePathFor(manager)
	if pathErr != nil {
		return pathErr
	}

	// Stop and disable service
	if fileExists(serviceFilePath) {
		if err := runManagedService(manager, "stop", false); err != nil {
			warnings = append(warnings, fmt.Sprintf("stop service: %v", err))
		}
		if err := runManagedService(manager, "disable", false); err != nil {
			warnings = append(warnings, fmt.Sprintf("disable service: %v", err))
		}
		if err := os.Remove(serviceFilePath); err != nil {
			warnings = append(warnings, fmt.Sprintf("remove service file: %v", err))
		}
		reloadServiceManager(manager)
	}

	// 仅删除已登记目录中的程序和管理入口，不删除用户选择的目录。
	for _, p := range []string{paths.installedBinary(), paths.cli(), filepath.Join("/usr/bin", filepath.Base(paths.cli())), defaultBinDirFile} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("remove %s: %v", p, err))
		}
	}

	if purge {
		if err := os.RemoveAll(defaultInstallRoot); err != nil {
			warnings = append(warnings, fmt.Sprintf("remove %s: %v", defaultInstallRoot, err))
		} else {
			fmt.Printf("Removed %s\n", defaultInstallRoot)
		}
	} else {
		os.Remove(defaultMetaPath)
		fmt.Printf("Config preserved under %s\n", defaultInstallRoot)
	}

	if len(warnings) > 0 {
		fmt.Println("Uninstall completed with warnings:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
		return nil
	}

	fmt.Println("Uninstall complete")
	return nil
}

func ensureRoot(cmd string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s requires root privileges; run with sudo", cmd)
	}
	return nil
}

func resolveDownloadURL(artifact, version string) string {
	if version == "latest" {
		return downloadBase + "/latest/download/" + artifact
	}
	return downloadBase + "/download/" + version + "/" + artifact
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func verifyReleaseChecksum(path, artifact string, checksums []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == artifact {
			expected = fields[0]
			break
		}
	}
	if len(expected) != 64 {
		return fmt.Errorf("checksum for %s is missing or invalid", artifact)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s for checksum: %w", artifact, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("checksum %s: %w", artifact, err)
	}
	actual := fmt.Sprintf("%x", h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s", artifact)
	}
	return nil
}

func installedVersion(requested string, report []byte) string {
	if requested != "" && requested != "latest" {
		return requested
	}
	fields := strings.Fields(string(report))
	if len(fields) >= 2 {
		return fields[1]
	}
	return "unknown"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func parseOutput(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--output" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--output=") {
			return strings.TrimPrefix(args[i], "--output=")
		}
	}
	return "text"
}

func parseRemoveNodeArgs(args []string) (string, int, error) {
	var panel string
	var nodeID int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--panel", "-a", "--api":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --panel")
			}
			panel = args[i+1]
			i++
		case "--node-id", "-n":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --node-id")
			}
			var err error
			nodeID, err = parsePositiveInt(args[i+1], "node-id")
			if err != nil {
				return "", 0, err
			}
			i++
		default:
			return "", 0, fmt.Errorf("unknown remove-node arg: %s", args[i])
		}
	}
	if strings.TrimSpace(panel) == "" || nodeID <= 0 {
		return "", 0, errors.New("usage: yz-agent bind remove-node --panel URL --node-id ID")
	}
	return strings.TrimSpace(panel), nodeID, nil
}

func parseRemoveMachineArgs(args []string) (string, int, error) {
	var panel string
	var machineID int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--panel", "-a", "--api":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --panel")
			}
			panel = args[i+1]
			i++
		case "--machine-id":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --machine-id")
			}
			var err error
			machineID, err = parsePositiveInt(args[i+1], "machine-id")
			if err != nil {
				return "", 0, err
			}
			i++
		default:
			return "", 0, fmt.Errorf("unknown remove-machine arg: %s", args[i])
		}
	}
	if strings.TrimSpace(panel) == "" || machineID <= 0 {
		return "", 0, errors.New("usage: yz-agent bind remove-machine --panel URL --machine-id ID")
	}
	return strings.TrimSpace(panel), machineID, nil
}

func parsePositiveInt(raw string, field string) (int, error) {
	var value int
	_, err := fmt.Sscanf(raw, "%d", &value)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s: %s", field, raw)
	}
	return value, nil
}

func removeBinding(panelURL string, nodeID int, machineID int, instanceID string) error {
	root, err := loadWritableRootConfig(defaultConfigPath)
	if err != nil {
		return err
	}
	instances := normalizeRootInstances(root)
	if len(instances) == 0 {
		return errors.New("no instances configured")
	}

	kept := make([]config.Config, 0, len(instances))
	removed := make([]config.Config, 0, 1)
	for _, inst := range instances {
		var matched bool
		if instanceID != "" {
			// Match by instance ID
			id, _ := inst.AutoInstanceID()
			matched = id == instanceID || inst.InstanceID == instanceID
		} else {
			matched = strings.TrimSpace(inst.Panel.URL) == strings.TrimSpace(panelURL)
			if nodeID > 0 {
				matched = matched && !inst.IsMachineMode() && inst.Panel.NodeID == nodeID
			}
			if machineID > 0 {
				matched = matched && inst.IsMachineMode() && inst.Machine != nil && inst.Machine.MachineID == machineID
			}
		}
		if matched {
			removed = append(removed, inst)
			continue
		}
		kept = append(kept, inst)
	}
	if len(removed) == 0 {
		if instanceID != "" {
			return fmt.Errorf("binding not found: id=%s", instanceID)
		}
		if nodeID > 0 {
			return fmt.Errorf("binding not found: panel=%s node_id=%d", panelURL, nodeID)
		}
		return fmt.Errorf("binding not found: panel=%s machine_id=%d", panelURL, machineID)
	}
	if len(kept) == 0 {
		// Last binding removed — stop the service to prevent crash-loop
		root.Instances = nil
		if err := writeRootConfig(defaultConfigPath, root); err != nil {
			return err
		}
		if err := pruneCredentialKeys(defaultCredentialsPath, removed); err != nil {
			return err
		}
		if err := writeInstallMeta(defaultMetaPath, root); err != nil {
			return err
		}
		runDetectedManagedService("stop", false)
		fmt.Printf("removed %d binding(s)\n", len(removed))
		fmt.Println("All bindings removed. Service stopped.")
		fmt.Println("Use 'yz-agent bind add-node/add-machine' to add a new binding, or 'yz-agent uninstall' to fully uninstall.")
		return nil
	}

	root.Instances = kept
	// Preserve top-level shared settings (log, kernel, etc.) — instances inherit from these.
	if err := writeRootConfig(defaultConfigPath, root); err != nil {
		return err
	}
	if err := pruneCredentialKeys(defaultCredentialsPath, removed); err != nil {
		return err
	}
	if err := writeInstallMeta(defaultMetaPath, root); err != nil {
		return err
	}
	if err := runDetectedManagedService("restart", false); err != nil {
		return err
	}
	fmt.Printf("removed %d binding(s)\n", len(removed))
	return nil
}

func loadWritableRootConfig(path string) (*config.RootConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	rc := &config.RootConfig{}
	if err := yaml.Unmarshal(data, rc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(rc.Instances) == 0 {
		legacy := &config.Config{}
		if err := yaml.Unmarshal(data, legacy); err != nil {
			return nil, fmt.Errorf("parse legacy config: %w", err)
		}
		rc.Config = *legacy
	}
	return rc, nil
}

func normalizeRootInstances(root *config.RootConfig) []config.Config {
	if len(root.Instances) > 0 {
		return append([]config.Config(nil), root.Instances...)
	}
	// Only treat legacy single-config mode if the embedded Config is valid
	if root.Config.Panel.URL != "" {
		return []config.Config{root.Config}
	}
	return nil
}

func writeRootConfig(path string, root *config.RootConfig) error {
	instances := root.Instances
	if len(instances) == 0 && root.Config.Panel.URL != "" {
		instances = []config.Config{root.Config}
	}
	out := fileRootConfig{Instances: make([]fileInstance, 0, len(instances))}

	// Preserve top-level shared settings so instances can inherit them.
	p := &root.Config
	if p.Log.Level != "" || p.Log.Output != "" {
		out.Log = &fileLogConfig{Level: p.Log.Level, Output: p.Log.Output}
	}
	if p.Kernel.Type != "" || p.Kernel.LogLevel != "" || p.Kernel.RealityMinClientVer != "" {
		out.Kernel = &fileKernelConfig{
			Type:                p.Kernel.Type,
			LogLevel:            p.Kernel.LogLevel,
			RealityMinClientVer: p.Kernel.RealityMinClientVer,
		}
	}
	if p.Node.PushInterval != 0 || p.Node.PullInterval != 0 || p.Node.TrackInterval != 0 || p.Node.DeviceReportInterval != 0 {
		out.Node = &fileNodeConfig{
			PushInterval:         p.Node.PushInterval,
			PullInterval:         p.Node.PullInterval,
			TrackInterval:        p.Node.TrackInterval,
			DeviceReportInterval: p.Node.DeviceReportInterval,
		}
	}
	if p.Runtime.GoMemLimit != "" || p.Runtime.GoGCPercent != 0 {
		out.Runtime = &fileRuntimeConfig{GoMemLimit: p.Runtime.GoMemLimit, GoGCPercent: p.Runtime.GoGCPercent}
	}
	if p.WS.StatusInterval != 0 || p.WS.HandshakeTimeout != 0 || p.WS.BackoffInitial != 0 {
		out.WS = &p.WS
	}
	if p.Cert.CertMode != "" || p.Cert.Domain != "" || p.Cert.CertFile != "" || p.Cert.AutoTLS {
		out.Cert = &p.Cert
	}
	if hasTimeSyncConfig(p.TimeSync) {
		out.TimeSync = &p.TimeSync
	}
	if hasFirewallConfig(p.Firewall) {
		out.Firewall = &p.Firewall
	}

	for _, inst := range instances {
		fi := fileInstance{
			ID: inst.InstanceID,
			Panel: filePanelConfig{
				URL:      inst.Panel.URL,
				TokenEnv: inst.Panel.TokenEnv,
				NodeID:   inst.Panel.NodeID,
				NodeType: inst.Panel.NodeType,
			},
			Kernel: fileKernelConfig{
				Type:                inst.Kernel.Type,
				ConfigDir:           inst.Kernel.ConfigDir,
				LogLevel:            inst.Kernel.LogLevel,
				GeoDataDir:          inst.Kernel.GeoDataDir,
				CustomConfig:        inst.Kernel.CustomConfig,
				CustomRoute:         inst.Kernel.CustomRoute,
				CustomOut:           inst.Kernel.CustomOutbound,
				RealityMinClientVer: inst.Kernel.RealityMinClientVer,
			},
			Log: fileLogConfig{
				Level:  inst.Log.Level,
				Output: inst.Log.Output,
			},
			HealthPort: inst.HealthPort,
		}
		if !inst.IsMachineMode() && (inst.Node.PushInterval != 0 || inst.Node.PullInterval != 0 || inst.Node.TrackInterval != 0 || inst.Node.DeviceReportInterval != 0) {
			fi.Node = &fileNodeConfig{
				PushInterval:         inst.Node.PushInterval,
				PullInterval:         inst.Node.PullInterval,
				TrackInterval:        inst.Node.TrackInterval,
				DeviceReportInterval: inst.Node.DeviceReportInterval,
			}
		}
		if inst.Runtime.GoMemLimit != "" || inst.Runtime.GoGCPercent != 0 {
			fi.Runtime = &fileRuntimeConfig{
				GoMemLimit:  inst.Runtime.GoMemLimit,
				GoGCPercent: inst.Runtime.GoGCPercent,
			}
		}
		if inst.IsMachineMode() && inst.Machine != nil {
			fi.Machine = &fileMachineConfig{
				MachineID: inst.Machine.MachineID,
				TokenEnv:  inst.Machine.TokenEnv,
			}
			fi.Panel.NodeID = 0
			fi.Panel.NodeType = ""
		}
		if inst.Cert.CertMode != "" || inst.Cert.Domain != "" || inst.Cert.CertFile != "" || inst.Cert.AutoTLS {
			fi.Cert = &inst.Cert
		}
		if inst.WS.StatusInterval != 0 || inst.WS.HandshakeTimeout != 0 || inst.WS.BackoffInitial != 0 {
			fi.WS = &inst.WS
		}
		if hasTimeSyncConfig(inst.TimeSync) {
			fi.TimeSync = &inst.TimeSync
		}
		if hasFirewallConfig(inst.Firewall) {
			fi.Firewall = &inst.Firewall
		}
		if len(inst.Nodes) > 0 {
			fi.Nodes = inst.Nodes
		}
		out.Instances = append(out.Instances, fi)
	}
	data, err := yaml.Marshal(&out)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

func hasTimeSyncConfig(cfg config.TimeSyncConfig) bool {
	return cfg.Enabled != nil || len(cfg.Servers) > 0 || cfg.Interval != 0 || cfg.Timeout != 0 ||
		cfg.WarnOffset != 0 || cfg.ErrorOffset != 0 || cfg.CriticalOffset != 0
}

func hasFirewallConfig(cfg config.FirewallConfig) bool {
	return cfg.Enabled != nil || cfg.Backend != "" || cfg.RedirectBackend != "" || cfg.Zone != "" || cfg.StateDir != ""
}

func pruneCredentialKeys(path string, removed []config.Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read credentials: %w", err)
	}
	removeKeys := map[string]struct{}{}
	for _, inst := range removed {
		if env := strings.TrimSpace(inst.Panel.TokenEnv); env != "" {
			removeKeys[env] = struct{}{}
		}
		if inst.Machine != nil {
			if env := strings.TrimSpace(inst.Machine.TokenEnv); env != "" {
				removeKeys[env] = struct{}{}
			}
		}
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		key := trimmed
		if idx := strings.Index(trimmed, "="); idx >= 0 {
			key = trimmed[:idx]
		}
		if _, ok := removeKeys[key]; ok {
			continue
		}
		kept = append(kept, line)
	}
	output := strings.Join(kept, "\n")
	if output != "" {
		output += "\n"
	}
	return os.WriteFile(path, []byte(output), 0o600)
}

func writeInstallMeta(path string, root *config.RootConfig) error {
	ver := "unknown"
	if meta, err := loadInstallMeta(path); err == nil && strings.TrimSpace(meta.Version) != "" {
		ver = meta.Version
	}
	return writeInstallMetaVersioned(path, root, ver, "")
}

func instanceMode(inst config.Config) string {
	if inst.IsMachineMode() {
		return "machine"
	}
	return "node"
}

func collectInstanceRows() ([]instanceRow, error) {
	rows, err := collectRowsFromMeta()
	if err == nil && len(rows) > 0 {
		return rows, nil
	}
	return collectRowsFromConfig()
}

func collectRowsFromMeta() ([]instanceRow, error) {
	meta, err := loadInstallMeta(defaultMetaPath)
	if err != nil {
		return nil, err
	}
	serviceStatus := serviceState()
	healthStatus := healthStatus()
	rows := make([]instanceRow, 0, len(meta.Instances))
	for _, inst := range meta.Instances {
		rows = append(rows, instanceRow{
			ID:      inst.ID,
			Mode:    inst.Mode,
			Panel:   inst.PanelURL,
			Target:  formatTarget(inst.NodeID, inst.MachineID),
			Service: serviceStatus,
			Health:  healthStatus,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func collectRowsFromConfig() ([]instanceRow, error) {
	root, err := config.LoadRoot(defaultConfigPath)
	if err != nil {
		return nil, err
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		return nil, err
	}
	serviceStatus := serviceState()
	healthStatus := healthStatus()
	rows := make([]instanceRow, 0, len(instances))
	for _, inst := range instances {
		mode := "node"
		if inst.IsMachineMode() {
			mode = "machine"
		}
		rows = append(rows, instanceRow{
			ID:      inst.InstanceID,
			Mode:    mode,
			Panel:   inst.Panel.URL,
			Target:  formatTarget(intPtr(inst.Panel.NodeID), machineIDPtr(inst)),
			Service: serviceStatus,
			Health:  healthStatus,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func printRows(rows []instanceRow, output string) error {
	if output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODE\tPANEL\tTARGET\tSERVICE\tHEALTH")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", row.ID, row.Mode, row.Panel, row.Target, row.Service, row.Health)
	}
	tw.Flush()
	_, err := fmt.Print(buf.String())
	return err
}

func healthStatus() string {
	return instanceAwareHealth()
}

func instanceAwareHealth() string {
	port := 0
	if meta, err := loadInstallMeta(defaultMetaPath); err == nil {
		for _, inst := range meta.Instances {
			if inst.HealthPort > 0 {
				port = inst.HealthPort
				break
			}
		}
	}
	if port == 0 {
		if root, err := loadWritableRootConfig(defaultConfigPath); err == nil {
			// Check top-level health_port first (inherited by all instances)
			if root.HealthPort > 0 {
				port = root.HealthPort
			}
			if port == 0 {
				instances, _ := root.NormalizeInstances()
				for _, inst := range instances {
					if inst.HealthPort > 0 {
						port = inst.HealthPort
						break
					}
				}
			}
		}
	}
	if port == 0 {
		return "disabled"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "down"
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return "ok"
	}
	return "down"
}

func formatTarget(nodeID *int, machineID *int) string {
	if nodeID != nil && *nodeID > 0 {
		return fmt.Sprintf("node_id=%d", *nodeID)
	}
	if machineID != nil && *machineID > 0 {
		return fmt.Sprintf("machine_id=%d", *machineID)
	}
	return ""
}

func intPtr(v int) *int {
	if v <= 0 {
		return nil
	}
	vv := v
	return &vv
}

func latestInstanceID(instances []*config.Config) string {
	if len(instances) > 0 {
		id, _ := instances[len(instances)-1].AutoInstanceID()
		return id
	}
	return ""
}

func machineIDPtr(cfg *config.Config) *int {
	if cfg.Machine == nil || cfg.Machine.MachineID <= 0 {
		return nil
	}
	vv := cfg.Machine.MachineID
	return &vv
}

// ── config subcommand ─────────────────────────────────────────────────

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yz-agent config <init|health-port|kernel|refresh-meta|bin-dir>")
	}
	switch args[0] {
	case "init":
		return runConfigInit(args[1:])
	case "health-port":
		return runConfigHealthPort(args[1:])
	case "kernel":
		return runConfigKernel(args[1:])
	case "refresh-meta":
		return runConfigRefreshMeta(args[1:])
	case "bin-dir":
		return runConfigBinDir(args[1:])
	case "migrate-root":
		return runConfigMigrateRoot(args[1:])
	case "cleanup-firewall":
		if len(args) != 3 || args[1] != "--config" {
			return errors.New("cleanup-firewall requires --config PATH")
		}
		cfg, err := config.LoadFirewall(args[2])
		if err != nil {
			return err
		}
		manager, err := firewall.New(cfg, args[2])
		if err != nil {
			return err
		}
		return manager.Close()
	default:
		return fmt.Errorf("unknown config command: %s", args[0])
	}
}

// xrayUnsupportedInbounds lists inbound protocols that only sing-box can serve.
// Switching such a node to xray leaves it without an inbound, so the switch is
// refused unless the caller passes --force.
var xrayUnsupportedInbounds = map[string]bool{
	"tuic":   true,
	"naive":  true,
	"anytls": true,
	"mieru":  true,
	"socks":  true,
	"http":   true,
}

// readInstanceIDs returns the instance ids in file order. config.Config skips
// the id field when unmarshalling, so it has to be read separately.
func readInstanceIDs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed struct {
		Instances []struct {
			ID string `yaml:"id"`
		} `yaml:"instances"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	ids := make([]string, 0, len(parsed.Instances))
	for _, inst := range parsed.Instances {
		ids = append(ids, inst.ID)
	}
	return ids
}

// runConfigKernel switches instances between the xray and sing-box kernels.
//
// Without --instance every instance is updated; the command reports what it
// changed and leaves restarting the service to the caller.
func runConfigKernel(args []string) error {
	cfgPath := defaultConfigPath
	target := ""
	instanceID := ""
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				return errors.New("--config requires a path")
			}
			i++
			cfgPath = args[i]
		case "--instance":
			if i+1 >= len(args) {
				return errors.New("--instance requires an instance id")
			}
			i++
			instanceID = args[i]
		case "--force":
			force = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag: %s", args[i])
			}
			if target != "" {
				return errors.New("kernel type given more than once")
			}
			target = args[i]
		}
	}

	switch strings.ToLower(strings.TrimSpace(target)) {
	case "xray":
		target = "xray"
	case "singbox", "sing-box":
		target = "singbox"
	case "":
		return errors.New("usage: yz-agent config kernel <xray|singbox> [--instance ID] [--config PATH] [--force]")
	default:
		return fmt.Errorf("kernel must be xray or singbox, got %q", target)
	}

	root, err := loadWritableRootConfig(cfgPath)
	if err != nil {
		return err
	}
	instances := normalizeRootInstances(root)
	if len(instances) == 0 {
		return fmt.Errorf("no instance found in %s", cfgPath)
	}

	// InstanceID is yaml:"-", so it is empty after loading. Recover the ids the
	// file actually carries; otherwise --instance never matches and writing back
	// would rename or drop them. Only fall back to the derived id when the file
	// has none.
	fileIDs := readInstanceIDs(cfgPath)
	for i := range instances {
		if i < len(fileIDs) && fileIDs[i] != "" {
			instances[i].InstanceID = fileIDs[i]
			continue
		}
		if autoID, idErr := instances[i].AutoInstanceID(); idErr == nil {
			instances[i].InstanceID = autoID
		}
	}

	matched := 0
	changed := 0
	for i := range instances {
		if instanceID != "" && instances[i].InstanceID != instanceID {
			continue
		}
		matched++

		nodeType := strings.ToLower(strings.TrimSpace(instances[i].Panel.NodeType))
		if target == "xray" && nodeType != "" && xrayUnsupportedInbounds[nodeType] && !force {
			return fmt.Errorf(
				"instance %s serves %q, which the xray kernel cannot host; pass --force to switch anyway",
				instances[i].InstanceID, nodeType,
			)
		}

		from := instances[i].Kernel.Type
		if from == "" {
			from = "xray"
		}
		if from == target {
			fmt.Printf("%s: already %s\n", instances[i].InstanceID, target)
			continue
		}
		instances[i].Kernel.Type = target
		changed++
		fmt.Printf("%s: %s -> %s\n", instances[i].InstanceID, from, target)
	}

	if matched == 0 {
		return fmt.Errorf("instance %q not found in %s", instanceID, cfgPath)
	}
	if changed == 0 {
		return nil
	}

	root.Instances = instances
	if err := writeRootConfig(cfgPath, root); err != nil {
		return err
	}
	fmt.Printf("updated %d instance(s) in %s\n", changed, cfgPath)
	fmt.Println("restart the service to apply: yz-agent service restart")
	return nil
}

func runConfigRefreshMeta(args []string) error {
	configPath := defaultConfigPath
	metaPath := defaultMetaPath
	releaseVersion := ""
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		switch args[i] {
		case "--config":
			i++
			configPath = args[i]
		case "--meta":
			i++
			metaPath = args[i]
		case "--version":
			i++
			releaseVersion = args[i]
		}
	}
	if strings.TrimSpace(releaseVersion) == "" {
		return errors.New("--version is required")
	}
	root, err := loadWritableRootConfig(configPath)
	if err != nil {
		return err
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		return err
	}
	return writeInstallMetaVersioned(metaPath, root, releaseVersion, latestInstanceID(instances))
}

// runConfigInit generates/merges an instance into config.yml, writes
// credentials.env and install-meta.json. It replaces the Python PY_INST,
// PY_CFG, PY_ENV, and PY_META heredoc blocks in install.sh.
//
// Output (stdout, one per line):
//
//	INSTANCE_ID=<generated-id>
//	ENV_KEY=<credential-env-var-name>
func runConfigInit(args []string) error {
	var (
		configIn       string
		configOut      string
		credentialsIn  string
		credentialsOut string
		metaPath       string
		mode           string
		panelURL       string
		nodeID         int
		nodeType       string
		machineID      int
		kernelType     string
		kernelExplicit bool
		healthPort     int
		gomemlimit     string
		gogc           int
		installRoot    string
		token          string
		releaseVersion string
	)

	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		switch args[i] {
		case "--config":
			i++
			configIn = args[i]
		case "--output":
			i++
			configOut = args[i]
		case "--credentials-in":
			i++
			credentialsIn = args[i]
		case "--credentials-out":
			i++
			credentialsOut = args[i]
		case "--meta":
			i++
			metaPath = args[i]
		case "--mode":
			i++
			mode = args[i]
		case "--panel-url":
			i++
			panelURL = args[i]
		case "--node-id":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --node-id: %w", err)
			}
			nodeID = v
		case "--node-type":
			i++
			nodeType = args[i]
		case "--machine-id":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --machine-id: %w", err)
			}
			machineID = v
		case "--kernel":
			i++
			kernelType = args[i]
			kernelExplicit = true
		case "--health-port":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --health-port: %w", err)
			}
			healthPort = v
		case "--gomemlimit":
			i++
			gomemlimit = args[i]
		case "--gogc":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --gogc: %w", err)
			}
			gogc = v
		case "--install-root":
			i++
			installRoot = args[i]
		case "--token":
			i++
			token = args[i]
		case "--version":
			i++
			releaseVersion = args[i]
		}
	}

	if mode == "" {
		return errors.New("--mode is required (node or machine)")
	}
	if panelURL == "" {
		return errors.New("--panel-url is required")
	}
	if mode == "node" && nodeID <= 0 {
		return errors.New("--node-id is required for node mode")
	}
	if mode == "machine" && machineID <= 0 {
		return errors.New("--machine-id is required for machine mode")
	}

	// 只为新绑定选择默认内核；旧配置的空值仍保留历史 Xray 语义。
	if !kernelExplicit {
		kernelType = "singbox"
		if mode == "node" && strings.EqualFold(strings.TrimSpace(nodeType), "vless") {
			kernelType = "xray"
		}
	} else {
		switch strings.ToLower(strings.TrimSpace(kernelType)) {
		case "xray":
			kernelType = "xray"
		case "singbox", "sing-box":
			kernelType = "singbox"
		default:
			return fmt.Errorf("kernel must be xray or singbox, got %q", kernelType)
		}
	}

	// Build the new instance.
	inst := config.Config{
		Panel: config.PanelConfig{URL: panelURL},
		Kernel: config.KernelConfig{
			Type:     kernelType,
			LogLevel: "warn",
		},
		Log:        config.LogConfig{Level: "info", Output: "stdout"},
		HealthPort: healthPort,
	}

	if mode == "machine" {
		inst.Machine = &config.MachineConfig{MachineID: machineID}
	} else {
		inst.Panel.NodeID = nodeID
		if nodeType != "" {
			inst.Panel.NodeType = nodeType
		}
	}

	// Generate deterministic instance ID.
	instanceID, err := inst.AutoInstanceID()
	if err != nil {
		return fmt.Errorf("generate instance ID: %w", err)
	}
	inst.InstanceID = instanceID

	if installRoot == "" {
		installRoot = "/etc/yz-agent"
	}
	inst.Kernel.ConfigDir = filepath.Join(installRoot, "instances", instanceID)

	// Build credential env key.
	envKey := "INSTANCE_" + strings.ToUpper(strings.ReplaceAll(instanceID, "-", "_"))
	if mode == "machine" {
		envKey += "_MACHINE_TOKEN"
		inst.Machine.TokenEnv = envKey
	} else {
		envKey += "_API_KEY"
		inst.Panel.TokenEnv = envKey
	}

	if gomemlimit != "" {
		inst.Runtime.GoMemLimit = gomemlimit
	}
	if gogc > 0 {
		inst.Runtime.GoGCPercent = gogc
	}

	// Load existing config (if any).
	root := &config.RootConfig{}
	hasExisting := false
	if configIn != "" {
		if loaded, loadErr := loadWritableRootConfig(configIn); loadErr == nil {
			root = loaded
			hasExisting = len(root.Instances) > 0 || root.Config.Panel.URL != "" || root.Config.Kernel.Type != ""
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
	}

	// Normalise existing instance IDs so dedup works correctly.
	var instances []config.Config
	if hasExisting {
		instances = normalizeRootInstances(root)
		seen := make(map[string]bool)
		deduped := make([]config.Config, 0, len(instances))
		for _, existing := range instances {
			autoID, idErr := existing.AutoInstanceID()
			if idErr == nil && autoID != "" {
				existing.InstanceID = autoID
			}
			id := existing.InstanceID
			if id != "" && seen[id] {
				continue
			}
			if id != "" {
				seen[id] = true
			}
			deduped = append(deduped, existing)
		}
		instances = deduped
	}

	// Merge: replace if same ID exists, otherwise append.
	replaced := false
	for i, existing := range instances {
		if existing.InstanceID == instanceID {
			if !kernelExplicit {
				inst.Kernel = existing.Kernel
			}
			if strings.TrimSpace(nodeType) == "" {
				inst.Panel.NodeType = existing.Panel.NodeType
			}
			instances[i] = inst
			replaced = true
			break
		}
	}
	if !replaced {
		// 单节点安装常常只提供编号；只在新绑定缺少协议和显式内核时查询面板。
		if mode == "node" && !kernelExplicit && strings.TrimSpace(nodeType) == "" {
			if token == "" {
				return errors.New("无法确认新节点的默认内核：请提供 --token、--node-type 或 --kernel")
			}
			client := panel.NewClient(config.PanelConfig{URL: panelURL, Token: token, NodeID: nodeID})
			nodeConfig, fetchErr := client.GetConfig()
			if fetchErr != nil || nodeConfig == nil {
				return errors.New("面板未返回可用的节点配置，请检查面板地址、凭据和节点编号，或显式提供 --node-type / --kernel")
			}
			inst.Panel.NodeType = strings.ToLower(strings.TrimSpace(nodeConfig.Protocol))
			if inst.Panel.NodeType == "" {
				return errors.New("面板未返回节点协议，无法确认默认内核")
			}
			switch strings.ToLower(strings.TrimSpace(nodeConfig.KernelType)) {
			case "xray":
				inst.Kernel.Type = "xray"
			case "singbox", "sing-box":
				inst.Kernel.Type = "singbox"
			case "":
				if inst.Panel.NodeType == "vless" {
					inst.Kernel.Type = "xray"
				}
			default:
				return errors.New("面板返回了不支持的节点内核，请先修正节点配置")
			}
		}
		instances = append(instances, inst)
	}
	root.Instances = instances

	// Write config.
	if configOut == "" {
		configOut = configIn
	}
	if configOut == "" {
		return errors.New("--output (or --config) is required")
	}
	if err := writeRootConfig(configOut, root); err != nil {
		return err
	}

	// Write credentials.
	if token != "" && credentialsOut != "" {
		if err := mergeCredentials(credentialsIn, credentialsOut, envKey, token); err != nil {
			return err
		}
	}

	// Write install-meta.json.
	if metaPath != "" {
		if err := writeInstallMetaVersioned(metaPath, root, releaseVersion, instanceID); err != nil {
			return err
		}
	}

	// Output for bash capture.
	fmt.Printf("INSTANCE_ID=%s\n", instanceID)
	fmt.Printf("ENV_KEY=%s\n", envKey)
	return nil
}

// mergeCredentials reads existing key=value credentials, adds/replaces
// the given key, and writes the result preserving insertion order.
func mergeCredentials(srcPath, dstPath, key, value string) error {
	entries := make(map[string]string)
	var order []string
	if srcPath != "" {
		data, err := os.ReadFile(srcPath)
		if err == nil {
			for _, raw := range strings.Split(string(data), "\n") {
				line := strings.TrimSpace(raw)
				if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
					continue
				}
				k, v, _ := strings.Cut(line, "=")
				if _, exists := entries[k]; !exists {
					order = append(order, k)
				}
				entries[k] = v
			}
		}
	}
	if _, exists := entries[key]; !exists {
		order = append(order, key)
	}
	entries[key] = value

	var buf strings.Builder
	for _, k := range order {
		fmt.Fprintf(&buf, "%s=%s\n", k, entries[k])
	}
	return os.WriteFile(dstPath, []byte(buf.String()), 0o600)
}

// writeInstallMetaVersioned writes install-meta.json with an explicit
// version and latest-instance-ID. When latestID is empty, the last
// instance in the config is used (backward compat with writeInstallMeta).
func writeInstallMetaVersioned(path string, root *config.RootConfig, ver, latestID string) error {
	if ver == "" {
		ver = "unknown"
	}
	instances := normalizeRootInstances(root)
	items := make([]instanceSummary, 0, len(instances))
	for _, inst := range instances {
		id, err := inst.AutoInstanceID()
		if err != nil {
			return err
		}
		if latestID == "" {
			latestID = id
		}
		item := instanceSummary{
			ID:         id,
			PanelURL:   inst.Panel.URL,
			Mode:       instanceMode(inst),
			NodeID:     intPtr(inst.Panel.NodeID),
			MachineID:  machineIDPtr(&inst),
			HealthPort: inst.HealthPort,
		}
		if item.Mode == "machine" {
			item.NodeID = nil
		}
		items = append(items, item)
	}
	meta := installMeta{
		ConfigMode:       "instances",
		Version:          ver,
		LatestInstanceID: latestID,
		InstanceCount:    len(items),
		Instances:        items,
		UpdatedAt:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal install meta: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// runConfigHealthPort reads health_port from an existing config file and
// prints it to stdout. Exits silently if the file does not exist or has
// no health_port.
func runConfigHealthPort(args []string) error {
	cfgPath := defaultConfigPath
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			i++
			cfgPath = args[i]
		}
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil // file not found → no health port
	}
	root := &config.RootConfig{}
	if err := yaml.Unmarshal(data, root); err != nil {
		return nil
	}
	// Check instances first, then legacy top-level.
	if len(root.Instances) > 0 {
		for _, inst := range root.Instances {
			if inst.HealthPort > 0 {
				fmt.Println(inst.HealthPort)
				return nil
			}
		}
	}
	if root.Config.HealthPort > 0 {
		fmt.Println(root.Config.HealthPort)
	}
	return nil
}
