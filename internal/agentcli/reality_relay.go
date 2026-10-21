package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/realityrelay"
)

func runExperiment(args []string) error {
	if len(args) == 0 || args[0] != "reality-relay" {
		return errors.New("用法: yz-agent experiment reality-relay --config PATH [--check]")
	}
	flags := flag.NewFlagSet("experiment reality-relay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "实验中转 YAML 路径")
	check := flags.Bool("check", false, "只校验配置")
	interval := flags.Duration("stats-interval", 10*time.Second, "无敏感信息的 JSON 统计输出间隔")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("实验中转参数无效；使用 --config PATH、--check、--stats-interval DURATION")
	}
	if flags.NArg() != 0 || *path == "" || *interval < time.Second || *interval > time.Hour {
		return errors.New("必须指定配置文件，统计间隔须在 1 秒至 1 小时内")
	}
	// 在读取初始配置前记录版本，避免初始化期间的修改被监视器当成旧配置而漏掉。
	var initialMod time.Time
	if st, e := os.Stat(*path); e == nil {
		initialMod = st.ModTime()
	}
	cfg, err := realityrelay.LoadConfig(*path)
	if err != nil {
		return err
	}
	server, err := realityrelay.New(cfg)
	if err != nil {
		return err
	}
	defer server.Close()
	if *check {
		fmt.Println("实验中转配置校验通过（未启动监听）")
		return nil
	}
	address, err := net.ResolveTCPAddr("tcp", server.ListenAddress())
	if err != nil {
		return errors.New("实验入口监听地址无效")
	}
	listener, err := net.ListenTCP("tcp", address)
	if err != nil {
		return fmt.Errorf("实验入口监听失败: %w", err)
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var outputMu sync.Mutex
	printStats := func(event string) {
		outputMu.Lock()
		defer outputMu.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Event string             `json:"event"`
			Stats realityrelay.Stats `json:"stats"`
		}{event, server.Stats()})
	}
	fmt.Printf("实验 REALITY 中转已监听 %s；配置变化后重新校验并撤销旧连接\n", listener.Addr())
	statsDone := make(chan struct{})
	go func() {
		defer close(statsDone)
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		// 只监视本命令明确指定的配置文件；不接触现有 Agent 实例及系统防火墙。
		watcher := relayConfigWatcher{appliedMod: initialMod}
		watch := time.NewTicker(time.Second)
		defer watch.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				printStats("stats")
			case <-watch.C:
				loaded, e := watcher.check(*path, server.Reload, time.Now())
				if e != nil {
					fmt.Fprintln(os.Stderr, "实验中转重载失败，保留旧配置:", e)
				} else if loaded {
					printStats("reloaded")
				}
			}
		}
	}()
	err = server.Serve(ctx, listener)
	stop()
	<-statsDone
	printStats("stopped")
	return err
}

type relayConfigWatcher struct {
	appliedMod  time.Time
	observedMod time.Time
	retryAt     time.Time
	retryDelay  time.Duration
	lastError   string
}

func (w *relayConfigWatcher) check(path string, reload func(realityrelay.Config) error, now time.Time) (bool, error) {
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Equal(w.appliedMod) {
		return false, nil
	}
	if !st.ModTime().Equal(w.observedMod) {
		w.observedMod = st.ModTime()
		w.retryAt = time.Time{}
		w.retryDelay = 0
		w.lastError = ""
	}
	if now.Before(w.retryAt) {
		return false, nil
	}
	next, err := realityrelay.LoadConfig(path)
	if err == nil {
		err = reload(next)
	}
	if err != nil {
		if w.retryDelay == 0 {
			w.retryDelay = time.Second
		} else {
			w.retryDelay = min(2*w.retryDelay, 30*time.Second)
		}
		w.retryAt = now.Add(w.retryDelay)
		if message := err.Error(); message != w.lastError {
			w.lastError = message
			return false, err
		}
		return false, nil
	}
	w.appliedMod = st.ModTime()
	w.retryAt = time.Time{}
	w.retryDelay = 0
	w.lastError = ""
	return true, nil
}
