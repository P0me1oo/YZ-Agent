package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/agentcli"
	"github.com/P0me1oo/YZ-Agent/internal/buildinfo"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/machine"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/service"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

var (
	version       = "v1.19.0"
	buildTime     = "unknown"
	commit        = "unknown"
	processBootID = fmt.Sprintf("%x", randomBootID())
)

func randomBootID() []byte {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	return id
}

// 退出最多需要等待在途报告、失败重试和最终报告各 30 秒，以及内核排空。
const shutdownGracePeriod = 2 * time.Minute

func main() {
	args := os.Args[1:]
	// 显式 run 启动节点；保留旧服务及 Docker 使用的 -c 参数入口。
	if len(args) > 0 && args[0] == "run" {
		os.Args = append([]string{os.Args[0]}, args[1:]...)
	} else if len(args) == 0 || !strings.HasPrefix(args[0], "-") || args[0] == "-h" || args[0] == "--help" || args[0] == "--version" {
		if err := agentcli.Run(args, version, buildTime, commit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	configPath := flag.String("c", "config.yml", "config file path")
	showVersion := flag.Bool("v", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Report("yz-agent", version, buildTime, commit))
		os.Exit(0)
	}

	rootCfg, err := config.LoadRoot(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	instances, err := rootCfg.NormalizeInstances()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to normalize config: %v\n", err)
		os.Exit(1)
	}
	config.InitLogger(instances[0].Log)

	// Apply runtime memory tuning before anything else allocates.
	applyRuntimeConfig(instances[0].Runtime)

	runWithReload(rootCfg, *configPath)
}

// runWithReload restarts all node services when the config file changes.
func runWithReload(initialRoot *config.RootConfig, configPath string) {
	var healthSrv *http.Server
	var healthPort int
	health := newHealthTracker()
	startHealth := func(port int) {
		healthPort = port
		if port <= 0 {
			return
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			nlog.Core().Error("failed to start health check listener", "port", port, "error", err)
			os.Exit(1)
		}
		mux := http.NewServeMux()
		mux.Handle("/healthz", health)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		healthSrv = srv
		go func() {
			nlog.Core().Debug(fmt.Sprintf("health check listening on :%d", port))
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				nlog.Core().Warn("health check server stopped", "error", err)
			}
		}()
	}

	initialInstances, err := initialRoot.NormalizeInstances()
	if err != nil {
		nlog.Core().Error("failed to normalize initial config", "error", err)
		os.Exit(1)
	}
	startHealth(initialInstances[0].HealthPort)
	defer func() {
		if healthSrv != nil {
			healthSrv.Close()
		}
	}()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for root := initialRoot; ; {
		ctx, cancel := context.WithCancel(context.Background())
		reloadCh := make(chan *config.RootConfig, 1)

		watcher, err := config.WatchConfigRoot(ctx, configPath, func(newCfg *config.RootConfig) {
			select {
			case reloadCh <- newCfg:
			default:
			}
		})
		if err != nil {
			nlog.Core().Warn("config watcher unavailable, hot-reload disabled", "error", err)
		}

		instances, err := root.NormalizeInstances()
		if err != nil {
			nlog.Core().Error("failed to normalize config", "error", err)
			os.Exit(1)
		}
		if err := config.ValidateStartupLayout(instances); err != nil {
			nlog.Core().Error("startup layout validation failed", "error", err)
			os.Exit(1)
		}
		timeManager := timesync.New(instances[0].TimeSync)
		firewallManager, err := firewall.New(instances[0].Firewall, configPath)
		if err != nil {
			nlog.Core().Error("防火墙管理器初始化失败", "error", err)
			os.Exit(1)
		}
		firewallManager.Start(ctx)
		stopShared := func() {
			timeManager.Stop()
			if err := firewallManager.Close(); err != nil {
				nlog.Core().Error("防火墙规则未完全清理，已保留恢复记录", "error", err)
			}
		}
		timesync.SetDefault(timeManager)
		health.setClock(timeManager)
		timeManager.Start(ctx)

		if instances[0].HealthPort != healthPort {
			if healthSrv != nil {
				healthSrv.Close()
				healthSrv = nil
			}
			startHealth(instances[0].HealthPort)
		}

		instanceHealthIDs, allHealthIDs := healthComponentIDs(instances)
		health.reset(allHealthIDs)

		errCh := make(chan error, 1)
		reportError := func(err error) {
			select {
			case errCh <- err:
			default:
			}
			// 单个节点或实例初始化失败不取消其他节点的共享上下文。
		}
		doneCh := make(chan struct{})
		var wg sync.WaitGroup
		for instanceIndex, instanceCfg := range instances {
			instanceCfg := instanceCfg
			healthIDs := instanceHealthIDs[instanceIndex]
			wg.Add(1)
			go func() {
				defer wg.Done()
				if instanceCfg.IsMachineMode() {
					nlog.Core().Info("starting machine instance", "instance", instanceCfg.InstanceID, "machine_id", instanceCfg.Machine.MachineID, "panel_url", instanceCfg.Panel.URL)
					orch := machine.New(instanceCfg)
					orch.SetAgentRuntime(version, processBootID)
					orch.SetFirewallController(firewallManager)
					orch.SetStatusHandler(func(status service.RuntimeStatus) {
						health.set(healthIDs[0], status)
					})
					if err := orch.Run(ctx); err != nil {
						nlog.Core().Error("machine instance exited with error", "instance", instanceCfg.InstanceID, "error", err)
						reportError(err)
					}
					return
				}
				nodes := instanceCfg.ExpandNodes()
				nlog.Core().Info("starting node instance", "instance", instanceCfg.InstanceID, "nodes", len(nodes), "panel_url", instanceCfg.Panel.URL)
				var instanceWG sync.WaitGroup
				for idx, nodeCfg := range nodes {
					nodeCfg := nodeCfg
					healthID := healthIDs[idx]
					instanceWG.Add(1)
					go func(idx int) {
						defer instanceWG.Done()
						if idx > 0 {
							delay := time.Duration(idx) * 250 * time.Millisecond
							if delay > 2*time.Second {
								delay = 2 * time.Second
							}
							select {
							case <-time.After(delay):
							case <-ctx.Done():
								return
							}
						}
						svc := service.New(nodeCfg)
						svc.SetFirewallController(firewallManager)
						svc.SetStatusHandler(func(status service.RuntimeStatus) {
							health.set(healthID, status)
						})
						if err := svc.Run(ctx); err != nil {
							nlog.Core().Error("node service exited with error", "instance", nodeCfg.InstanceID, "node_id", nodeCfg.Panel.NodeID, "error", err)
							reportError(err)
						}
					}(idx)
				}
				instanceWG.Wait()
			}()
		}
		go func() { wg.Wait(); close(doneCh) }()

		var newRoot *config.RootConfig
		select {
		case newRoot = <-reloadCh:
			nlog.Core().Info("config changed, restarting all services...")
			cancel()
			if sig, shuttingDown := waitForReloadStop(doneCh, sigCh); shuttingDown {
				nlog.Core().Info(fmt.Sprintf("received %v during reload, shutting down...", sig))
				forceExitIfNeeded(waitForShutdown(doneCh, sigCh, shutdownGracePeriod))
				if watcher != nil {
					watcher.Stop()
				}
				stopShared()
				return
			}
		case sig := <-sigCh:
			nlog.Core().Info(fmt.Sprintf("received %v, shutting down...", sig))
			cancel()
			forceExitIfNeeded(waitForShutdown(doneCh, sigCh, shutdownGracePeriod))
			if watcher != nil {
				watcher.Stop()
			}
			stopShared()
			return
		case <-doneCh:
		}

		cancel()
		if watcher != nil {
			watcher.Stop()
		}
		stopShared()

		if newRoot == nil {
			close(errCh)
			if err := <-errCh; err != nil {
				os.Exit(1)
			}
			nlog.Core().Info("stopped")
			return
		}

		newInstances, err := newRoot.NormalizeInstances()
		if err != nil {
			nlog.Core().Error("failed to normalize reloaded config", "error", err)
			os.Exit(1)
		}
		config.InitLogger(newInstances[0].Log)
		applyRuntimeConfig(newInstances[0].Runtime)
		root = newRoot
		nlog.Core().Info("reload complete, services restarting with new config")
	}
}

type shutdownResult int

const (
	shutdownComplete shutdownResult = iota
	shutdownSecondSignal
	shutdownTimedOut
)

func waitForReloadStop(done <-chan struct{}, signals <-chan os.Signal) (os.Signal, bool) {
	select {
	case <-done:
		return nil, false
	case sig := <-signals:
		return sig, true
	}
}

func waitForShutdown(done <-chan struct{}, signals <-chan os.Signal, timeout time.Duration) shutdownResult {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return shutdownComplete
	case <-signals:
		return shutdownSecondSignal
	case <-timer.C:
		return shutdownTimedOut
	}
}

func forceExitIfNeeded(result shutdownResult) {
	switch result {
	case shutdownSecondSignal:
		nlog.Core().Warn("received second signal, forcing exit")
		os.Exit(1)
	case shutdownTimedOut:
		nlog.Core().Error("shutdown timed out, forcing exit", "timeout", shutdownGracePeriod)
		os.Exit(2)
	}
}

func healthComponentIDs(instances []*config.Config) ([][]string, []string) {
	perInstance := make([][]string, len(instances))
	var all []string
	for instanceIndex, instanceCfg := range instances {
		if instanceCfg.IsMachineMode() {
			id := fmt.Sprintf("instance/%d/machine", instanceIndex)
			perInstance[instanceIndex] = []string{id}
			all = append(all, id)
			continue
		}
		nodes := instanceCfg.ExpandNodes()
		ids := make([]string, len(nodes))
		for nodeIndex := range nodes {
			ids[nodeIndex] = fmt.Sprintf("instance/%d/node/%d", instanceIndex, nodeIndex)
		}
		perInstance[instanceIndex] = ids
		all = append(all, ids...)
	}
	return perInstance, all
}

// applyRuntimeConfig wires up Go runtime memory limits from the config file.
// Both settings can also be overridden by environment variables (GOMEMLIMIT /
// GOGC) — the env vars take precedence because Go's runtime reads them before
// we can call these functions, but we set them here for completeness and so
// the values are logged.
func applyRuntimeConfig(rt config.RuntimeConfig) {
	// GOGC
	if rt.GoGCPercent > 0 {
		prev := debug.SetGCPercent(rt.GoGCPercent)
		nlog.Core().Info("runtime: GOGC set", "gogc", rt.GoGCPercent, "prev", prev)
	}

	// GOMEMLIMIT — parse human-readable size string (e.g. "30MiB")
	if rt.GoMemLimit != "" {
		limit, err := parseMemLimit(rt.GoMemLimit)
		if err != nil {
			nlog.Core().Warn("runtime: invalid gomemlimit, ignoring", "value", rt.GoMemLimit, "error", err)
		} else {
			prev := debug.SetMemoryLimit(limit)
			nlog.Core().Info("runtime: GOMEMLIMIT set",
				"limit", rt.GoMemLimit,
				"bytes", limit,
				"prev_bytes", prev,
			)
		}
	}
}

// parseMemLimit converts a human-readable size string to bytes.
// Supported suffixes: B, KiB, MiB, GiB, TiB (case-insensitive).
func parseMemLimit(s string) (int64, error) {
	s = strings.TrimSpace(s)
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"B", 1},
	}
	upper := strings.ToUpper(s)
	for _, sf := range suffixes {
		if strings.HasSuffix(upper, strings.ToUpper(sf.suffix)) {
			numStr := strings.TrimSuffix(upper, strings.ToUpper(sf.suffix))
			numStr = strings.TrimSpace(numStr)
			var n int64
			if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
				return 0, fmt.Errorf("parse number %q: %w", numStr, err)
			}
			return n * sf.mult, nil
		}
	}
	// No suffix: treat as raw bytes.
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("unrecognised size format %q", s)
	}
	return n, nil
}
