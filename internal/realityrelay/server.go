package realityrelay

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type routeCounters struct {
	connections atomic.Uint64
	active      atomic.Int64
	upload      atomic.Uint64
	download    atomic.Uint64
	errors      atomic.Uint64
}

type RouteStats struct {
	Name        string `json:"name"`
	Connections uint64 `json:"connections"`
	Active      int64  `json:"active"`
	Upload      uint64 `json:"upload"`
	Download    uint64 `json:"download"`
	Errors      uint64 `json:"errors"`
}

// Stats 不包含客户端身份、目标、shortId 或任何密钥。字节数在连接结束时结算。
type Stats struct {
	Accepted      uint64       `json:"accepted"`
	Rejected      uint64       `json:"rejected"`
	Active        int64        `json:"active"`
	CopyMode      string       `json:"copy_mode"`
	SpliceCapable bool         `json:"splice_capable"`
	Routes        []RouteStats `json:"routes"`
}

type Server struct {
	current            atomic.Pointer[compiledConfig]
	mu                 sync.Mutex
	listener           *net.TCPListener
	connections        map[*net.TCPConn]struct{}
	cancel             context.CancelFunc
	started, closed    bool
	wg                 sync.WaitGroup
	slots, pending     chan struct{}
	accepted, rejected atomic.Uint64
	active             atomic.Int64
}

func New(cfg Config) (*Server, error) {
	c, err := compileConfig(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{connections: make(map[*net.TCPConn]struct{}), slots: make(chan struct{}, c.MaxConnections), pending: make(chan struct{}, c.MaxHandshakes)}
	s.current.Store(c)
	return s, nil
}

func (s *Server) ListenAddress() string { return s.current.Load().Listen }

func (s *Server) Stats() Stats {
	c := s.current.Load()
	out := Stats{Accepted: s.accepted.Load(), Rejected: s.rejected.Load(), Active: s.active.Load(), CopyMode: c.CopyMode, SpliceCapable: spliceSupported()}
	for _, r := range c.routes {
		out.Routes = append(out.Routes, RouteStats{Name: r.name, Connections: r.stats.connections.Load(), Active: r.stats.active.Load(), Upload: r.stats.upload.Load(), Download: r.stats.download.Load(), Errors: r.stats.errors.Load()})
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].Name < out.Routes[j].Name })
	return out
}

// Reload 先完整校验新配置；相同配置不打断连接。配置有变化时关闭旧连接，防止撤销后继续使用。
// 监听地址及并发容量属于进程布局，修改它们需要重新启动实验命令。
func (s *Server) Reload(cfg Config) error {
	next, err := compileConfig(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	old := s.current.Load()
	if next.Listen != old.Listen || next.MaxConnections != old.MaxConnections || next.MaxHandshakes != old.MaxHandshakes {
		return errors.New("修改监听地址或并发容量需要重启实验入口")
	}
	if next.fingerprint == old.fingerprint {
		return nil
	}
	previousCounters := make(map[string]*routeCounters, len(old.routes))
	for _, previous := range old.routes {
		previousCounters[previous.name] = previous.stats
	}
	for _, r := range next.routes {
		if counters := previousCounters[r.name]; counters != nil {
			r.stats = counters
		}
	}
	s.current.Store(next)
	for conn := range s.connections {
		_ = conn.Close()
	}
	return nil
}

func (s *Server) Serve(ctx context.Context, listener *net.TCPListener) error {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return errors.New("实验入口已启动或关闭")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.listener = listener
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-finished:
		}
	}()
	defer func() { _ = s.Close(); s.wg.Wait(); close(finished) }()
	var retryDelay time.Duration
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Temporary() || errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM) {
				if retryDelay == 0 {
					retryDelay = 5 * time.Millisecond
				} else {
					retryDelay = min(2*retryDelay, time.Second)
				}
				timer := time.NewTimer(retryDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
					continue
				}
			}
			return err
		}
		retryDelay = 0
		s.accepted.Add(1)
		select {
		case s.slots <- struct{}{}:
		default:
			s.rejected.Add(1)
			_ = conn.Close()
			continue
		}
		select {
		case s.pending <- struct{}{}:
		default:
			<-s.slots
			s.rejected.Add(1)
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			<-s.pending
			<-s.slots
			_ = conn.Close()
			return nil
		}
		s.connections[conn] = struct{}{}
		s.wg.Add(1)
		s.active.Add(1)
		s.mu.Unlock()
		go s.handle(ctx, conn)
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	for conn := range s.connections {
		_ = conn.Close()
	}
	return nil
}

func (s *Server) handle(ctx context.Context, client *net.TCPConn) {
	pending := true
	defer func() {
		_ = client.Close()
		s.mu.Lock()
		delete(s.connections, client)
		s.mu.Unlock()
		if pending {
			<-s.pending
		}
		<-s.slots
		s.active.Add(-1)
		s.wg.Done()
	}()
	c := s.current.Load()
	_ = client.SetNoDelay(true)
	_ = client.SetReadDeadline(time.Now().Add(c.HandshakeTimeout))
	prefix, hello, err := readHello(client)
	if err != nil || !c.names[hello.serverName] {
		s.rejected.Add(1)
		return
	}
	r, err := c.identify(hello, time.Now())
	if err != nil {
		if c.fallback == nil {
			s.rejected.Add(1)
			return
		}
		r = c.fallback
	}
	<-s.pending
	pending = false
	dialer := net.Dialer{Timeout: c.DialTimeout, KeepAlive: 30 * time.Second}
	remoteConn, err := dialer.DialContext(ctx, "tcp", r.target)
	if err != nil {
		r.stats.errors.Add(1)
		return
	}
	remote, ok := remoteConn.(*net.TCPConn)
	if !ok {
		_ = remoteConn.Close()
		r.stats.errors.Add(1)
		return
	}
	defer func() { _ = remote.Close(); s.mu.Lock(); delete(s.connections, remote); s.mu.Unlock() }()
	s.mu.Lock()
	if s.closed || s.current.Load() != c {
		s.mu.Unlock()
		return
	}
	s.connections[remote] = struct{}{}
	s.mu.Unlock()
	r.stats.connections.Add(1)
	r.stats.active.Add(1)
	defer r.stats.active.Add(-1)
	_ = remote.SetNoDelay(true)
	_ = remote.SetWriteDeadline(time.Now().Add(c.HandshakeTimeout))
	written, err := writeAll(remote, prefix)
	r.stats.upload.Add(uint64(written))
	if err != nil {
		r.stats.errors.Add(1)
		return
	}
	_ = remote.SetWriteDeadline(time.Time{})
	_ = client.SetReadDeadline(time.Time{})
	type result struct {
		bytes int64
		err   error
	}
	upload := make(chan result, 1)
	go func() {
		n, err := copyTCP(remote, client, c.CopyMode)
		_ = remote.CloseWrite()
		if err != nil {
			_ = client.Close()
			_ = remote.Close()
		}
		upload <- result{n, err}
	}()
	download, downloadErr := copyTCP(client, remote, c.CopyMode)
	_ = client.CloseWrite()
	if downloadErr != nil {
		_ = client.Close()
		_ = remote.Close()
	}
	u := <-upload
	r.stats.upload.Add(uint64(u.bytes))
	r.stats.download.Add(uint64(download))
	if downloadErr != nil || u.err != nil {
		r.stats.errors.Add(1)
	}
}

func writeAll(w io.Writer, b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n, err := w.Write(b)
		total += n
		b = b[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
