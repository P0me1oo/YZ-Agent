//go:build linux

package systemwg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type M = map[string]any

// Runtime 跟随一次核心实例生存；构造失败、启动失败和正常退出使用同一回收路径。
type Runtime struct {
	tunnels       []*tunnel
	once          sync.Once
	mu            sync.RWMutex
	closed        bool
	started       bool
	xrayLanding   string
	xrayOutbounds map[string]string
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		for _, t := range r.tunnels {
			t.close()
		}
	})
}

func (r *Runtime) Start() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("WG 线路已经关闭")
	}
	if r.started {
		r.mu.Unlock()
		return nil
	}
	var err error
	for _, t := range r.tunnels {
		if err = t.start(); err != nil {
			break
		}
	}
	r.started = err == nil
	r.mu.Unlock()
	if err != nil {
		r.Close()
		return fmt.Errorf("启动系统 WG 失败: %w", err)
	}
	return nil
}

// Prepare 仅改写本次生成的核心配置，不修改面板状态，也不把运行凭据写入磁盘。
func Prepare(nc *model.NodeSpec, kind string, cfg M) (_ *Runtime, err error) {
	r := &Runtime{xrayOutbounds: make(map[string]string)}
	defer func() {
		if err != nil {
			r.Close()
		}
	}()
	if nc == nil || nc.Relay == nil {
		return r, nil
	}
	if kind != "singbox" && kind != "xray" {
		return nil, errors.New("未知系统 WG 核心")
	}
	if nc.IsRelayLanding() && nc.Relay.Protocol == "wireguard" {
		w := nc.Relay.WireGuard
		if err := checkAddresses(w, false); err != nil {
			return nil, err
		}
		port := nc.Relay.ListenPort
		if port == 0 {
			port = nc.ServerPort
		}
		t, err := newTunnel(w, "", port)
		if err != nil {
			return nil, err
		}
		r.tunnels = append(r.tunnels, t)
		in := M{"type": "socks", "tag": "relay-in", "listen": "10.253.0.2", "listen_port": 1080, "netns": t.path()}
		if kind == "singbox" {
			cfg["inbounds"] = []M{in}
			removeEndpoint(cfg, "relay-in")
		} else {
			// Xray 的落地监听由 Node 直接注册到核心，不再经过本机代理。
			cfg["inbounds"] = []M{}
			r.xrayLanding = t.path()
		}
	}
	if nc.IsRelayEntry() {
		for _, child := range nc.Relay.Children {
			if child.Protocol != "wireguard" {
				continue
			}
			if err := checkAddresses(child.WireGuard, true); err != nil {
				return nil, err
			}
			t, err := newTunnel(child.WireGuard, net.JoinHostPort(child.Address, strconv.Itoa(child.Port)), 0)
			if err != nil {
				return nil, err
			}
			r.tunnels = append(r.tunnels, t)
			out := M{"type": "socks", "tag": child.Tag, "server": "10.253.0.2", "server_port": 1080, "version": "5", "netns": t.path()}
			if kind == "singbox" {
				removeEndpoint(cfg, child.Tag)
				cfg["outbounds"] = append(cfg["outbounds"].([]M), out)
			} else {
				r.xrayOutbounds[child.Tag] = t.path()
				outs := cfg["outbounds"].([]M)
				for i, old := range outs {
					if old["tag"] == child.Tag {
						outs[i] = M{"tag": child.Tag, "protocol": "socks", "settings": M{"servers": []M{{"address": "10.253.0.2", "port": 1080}}}}
					}
				}
			}
		}
	}
	return r, nil
}

func removeEndpoint(cfg M, tag string) {
	endpoints, ok := cfg["endpoints"].([]M)
	if !ok {
		return
	}
	kept := endpoints[:0]
	for _, endpoint := range endpoints {
		if endpoint["tag"] != tag {
			kept = append(kept, endpoint)
		}
	}
	if len(kept) == 0 {
		delete(cfg, "endpoints")
	} else {
		cfg["endpoints"] = kept
	}
}

// 现有面板固定分配这组地址。拒绝不匹配的独立配置，避免静默连接错误的隧道地址。
func checkAddresses(w *model.RelayWireGuardConfig, entry bool) error {
	if w == nil {
		return errors.New("缺少系统 WG 参数")
	}
	want := "10.253.0.2/32"
	if entry {
		want = "10.253.0.1/32"
	}
	for _, a := range w.Address {
		if a == want {
			return nil
		}
	}
	return errors.New("系统 WG 地址与面板分配规则不一致")
}

func (r *Runtime) XrayLandingNamespace() string            { return r.xrayLanding }
func (r *Runtime) XrayOutboundNamespace(tag string) string { return r.xrayOutbounds[tag] }

// 持锁直到套接字创建结束，避免关闭后复用的描述符指向另一条线路。
func (r *Runtime) XrayNamespaceCall(ctx context.Context, tag string, action func() error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return errors.New("WG 线路已经关闭")
	}
	path := r.xrayLanding
	if tag != "" {
		path = r.xrayOutbounds[tag]
	}
	if path == "" {
		return errors.New("WG 网络空间不存在")
	}
	return InNamespace(ctx, path, action)
}
