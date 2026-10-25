package balancer

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type Config struct {
	Network     string   `json:"network"`
	Targets     []Target `json:"targets"`
	MaxSessions int      `json:"max_sessions"`
}

// ReceiverConfig 只信任指定调度入口携带的 TCP PROXY 来源，不扩大普通节点的信任范围。
type ReceiverConfig struct {
	TrustedAddresses []string `json:"trusted_addresses"`
}

func CloneConfig(config *Config) *Config {
	if config == nil {
		return nil
	}
	clone := *config
	clone.Targets = append([]Target(nil), config.Targets...)
	return &clone
}
func CloneReceiver(config *ReceiverConfig) *ReceiverConfig {
	if config == nil {
		return nil
	}
	clone := *config
	clone.TrustedAddresses = append([]string(nil), config.TrustedAddresses...)
	return &clone
}

type Runtime struct {
	ctx         context.Context
	cancel      context.CancelFunc
	selector    *Selector
	config      Config
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]bool
	wg          sync.WaitGroup
	once        sync.Once
}

func Start(address string, config Config) (*Runtime, error) {
	if config.Network != "tcp" {
		return nil, errors.New("第一版调度入口只支持 VLESS Reality TCP")
	}
	selector, err := NewSelector(config.Targets)
	if err != nil {
		return nil, err
	}
	if config.MaxSessions == 0 {
		config.MaxSessions = 16384
	}
	if config.MaxSessions < 1 || config.MaxSessions > 1000000 {
		return nil, errors.New("前置池会话数量无效")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{ctx: ctx, cancel: cancel, selector: selector, config: config, listener: listener, connections: make(map[net.Conn]bool)}
	r.wg.Add(1)
	go r.acceptLoop()
	return r, nil
}
func (r *Runtime) UpdateTargets(targets []Target) error { return r.selector.Update(targets) }
func (r *Runtime) Addr() net.Addr                       { return r.listener.Addr() }
func (r *Runtime) Running() bool                        { return r.ctx.Err() == nil }
func (r *Runtime) Close() {
	r.once.Do(func() {
		r.cancel()
		r.listener.Close()
		r.mu.Lock()
		for conn := range r.connections {
			conn.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
}
func (r *Runtime) acceptLoop() {
	defer r.wg.Done()
	for {
		client, err := r.listener.Accept()
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			if temporary, ok := err.(net.Error); ok && temporary.Temporary() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			r.cancel()
			return
		}
		r.mu.Lock()
		if r.ctx.Err() != nil || len(r.connections) >= r.config.MaxSessions*2 {
			r.mu.Unlock()
			client.Close()
			continue
		}
		r.connections[client] = true
		r.wg.Add(1)
		r.mu.Unlock()
		go r.connection(client)
	}
}
func (r *Runtime) connection(client net.Conn) {
	defer r.wg.Done()
	defer func() { client.Close(); r.mu.Lock(); delete(r.connections, client); r.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	excluded := make(map[int]bool)
	var upstream net.Conn
	for {
		target, ok := r.selector.Pick(excluded)
		if !ok || ctx.Err() != nil {
			return
		}
		excluded[target.ID] = true
		attempt, stop := context.WithTimeout(ctx, 3*time.Second)
		var err error
		upstream, err = (&net.Dialer{}).DialContext(attempt, "tcp", target.Address)
		stop()
		if err != nil {
			continue
		}
		if err = writeProxyHeader(upstream, client.RemoteAddr(), client.LocalAddr()); err == nil {
			break
		}
		upstream.Close()
	}
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		upstream.Close()
		return
	}
	r.connections[upstream] = true
	r.mu.Unlock()
	defer func() { upstream.Close(); r.mu.Lock(); delete(r.connections, upstream); r.mu.Unlock() }()
	done := make(chan struct{}, 1)
	go func() { io.Copy(upstream, client); closeWrite(upstream); done <- struct{}{} }()
	io.Copy(client, upstream)
	closeWrite(client)
	<-done
}
func closeWrite(conn net.Conn) {
	if value, ok := conn.(interface{ CloseWrite() error }); ok {
		value.CloseWrite()
	}
}
