//go:build linux

package systemwg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

type M = map[string]any

// Runtime 跟随一次核心实例生存；构造失败、启动失败和正常退出使用同一回收路径。
type Runtime struct {
	tunnels []*tunnel
	bridges []*box.Box
	once    sync.Once
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		for _, b := range r.bridges {
			_ = b.Close()
		}
		for _, t := range r.tunnels {
			t.close()
		}
	})
}

func (r *Runtime) Start() error {
	if r == nil {
		return nil
	}
	for _, b := range r.bridges {
		if err := b.Start(); err != nil {
			r.Close()
			return fmt.Errorf("启动系统 WG 转接失败: %w", err)
		}
	}
	for _, t := range r.tunnels {
		if err := t.start(); err != nil {
			r.Close()
			return fmt.Errorf("启动系统 WG 失败: %w", err)
		}
	}
	return nil
}

// Prepare 仅改写本次生成的核心配置，不修改面板状态，也不把运行凭据写入磁盘。
func Prepare(nc *model.NodeSpec, kind string, cfg M) (_ *Runtime, err error) {
	r := &Runtime{}
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
			port, secret, err := localEndpoint()
			if err != nil {
				return nil, err
			}
			cfg["inbounds"] = []M{xraySocksInbound(port, secret)}
			out := M{"type": "socks", "server": "127.0.0.1", "server_port": port, "version": "5", "username": "wg", "password": secret}
			if err := r.bridge(in, out); err != nil {
				return nil, err
			}
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
				port, secret, err := localEndpoint()
				if err != nil {
					return nil, err
				}
				in := M{"type": "socks", "listen": "127.0.0.1", "listen_port": port, "users": []M{{"username": "wg", "password": secret}}}
				if err := r.bridge(in, out); err != nil {
					return nil, err
				}
				outs := cfg["outbounds"].([]M)
				for i, old := range outs {
					if old["tag"] == child.Tag {
						outs[i] = xraySocksOutbound(child.Tag, port, secret)
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

func localEndpoint() (int, string, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, "", err
	}
	return port, hex.EncodeToString(b[:]), nil
}

func xraySocksInbound(port int, secret string) M {
	return M{"tag": "relay-in", "protocol": "socks", "listen": "127.0.0.1", "port": port,
		"settings": M{"auth": "password", "accounts": []M{{"user": "wg", "pass": secret}}, "udp": true, "ip": "127.0.0.1"}}
}

func xraySocksOutbound(tag string, port int, secret string) M {
	return M{"tag": tag, "protocol": "socks", "settings": M{"servers": []M{{"address": "127.0.0.1", "port": port, "users": []M{{"user": "wg", "pass": secret}}}}}}
}

func (r *Runtime) bridge(in, out M) error {
	ctx := include.Context(context.Background())
	b, err := json.Marshal(M{"log": M{"disabled": true}, "inbounds": []M{in}, "outbounds": []M{out}})
	if err != nil {
		return err
	}
	opts, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, b)
	if err != nil {
		return err
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return err
	}
	r.bridges = append(r.bridges, instance)
	return nil
}
