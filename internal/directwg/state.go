package directwg

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"golang.org/x/time/rate"
)

// WG 没有 TCP 式断开通知，认证后的保活为来源续期；失联后回收名额。
const sourceTTL = 90 * time.Second
const globalTTL = 35 * time.Second

type userState struct {
	spec        model.UserSpec
	ctx         context.Context
	cancel      context.CancelFunc
	sources     map[string]time.Time
	latest      netip.Addr
	connections map[io.Closer]struct{}
}

type State struct {
	mu          sync.Mutex
	users       map[int]*userState
	byKey       map[string]int
	byAddress   map[netip.Addr]int
	totals      map[int][2]int64
	filter      *deviceip.Filter
	global      map[int][]string
	globalAt    time.Time
	speed       func(string) *rate.Limiter
	connLimiter model.ConnLimiter
}

func newState() *State {
	return &State{users: make(map[int]*userState), byKey: make(map[string]int),
		byAddress: make(map[netip.Addr]int), totals: make(map[int][2]int64), filter: &deviceip.Filter{}}
}

func validAt(u *userState, now time.Time) bool {
	return u != nil && (u.spec.WireGuard.ExpiresAt == 0 || now.Unix() < u.spec.WireGuard.ExpiresAt)
}

func validateUsers(users []model.UserSpec, serverAddresses []string) error {
	ids, keys, addresses := map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, v := range serverAddresses {
		addresses[netip.MustParsePrefix(v).Addr().String()] = true
	}
	for _, u := range users {
		if u.ID <= 0 || u.UUID == "" || ids[u.ID] || u.WireGuard == nil {
			return fmt.Errorf("WireGuard 用户身份缺失或重复")
		}
		ids[u.ID] = true
		if err := model.ValidateWireGuardKey(u.WireGuard.PublicKey); err != nil {
			return err
		}
		if keys[u.WireGuard.PublicKey] {
			return fmt.Errorf("WireGuard 用户公钥重复")
		}
		keys[u.WireGuard.PublicKey] = true
		if err := model.ValidateWireGuardAddresses(u.WireGuard.Address); err != nil {
			return err
		}
		for _, v := range u.WireGuard.Address {
			a := netip.MustParsePrefix(v).Addr().String()
			if addresses[a] {
				return fmt.Errorf("WireGuard 用户隧道地址重复")
			}
			addresses[a] = true
		}
		if u.WireGuard.ExpiresAt < 0 {
			return fmt.Errorf("WireGuard 到期时间无效")
		}
	}
	return nil
}

func (s *State) replaceUsers(users []model.UserSpec) {
	s.mu.Lock()
	next := make(map[int]*userState, len(users))
	var closeUsers []*userState
	for _, u := range users {
		u.WireGuard = model.CloneWireGuardPeer(u.WireGuard)
		previous := s.users[u.ID]
		if previous != nil && previous.spec.UUID == u.UUID && previous.spec.WireGuard.PublicKey == u.WireGuard.PublicKey && slices.Equal(previous.spec.WireGuard.Address, u.WireGuard.Address) && previous.ctx.Err() == nil {
			previous.spec = u
			next[u.ID] = previous
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			next[u.ID] = &userState{spec: u, ctx: ctx, cancel: cancel, sources: make(map[string]time.Time), connections: make(map[io.Closer]struct{})}
		}
	}
	for id, old := range s.users {
		if next[id] != old {
			old.cancel()
			closeUsers = append(closeUsers, old)
		}
	}
	s.users, s.byKey, s.byAddress = next, make(map[string]int), make(map[netip.Addr]int)
	for id, u := range next {
		s.byKey[u.spec.WireGuard.PublicKey] = id
		for _, v := range u.spec.WireGuard.Address {
			s.byAddress[netip.MustParsePrefix(v).Addr()] = id
		}
	}
	var connections []io.Closer
	for _, u := range closeUsers {
		for c := range u.connections {
			connections = append(connections, c)
		}
	}
	s.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
}

// 只能由 WG 完成认证和重放检查后的回调调用，未认证的 UDP 不占用设备额度。
func (s *State) admit(key string, source netip.Addr, payload bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admitLocked(key, source, payload, now)
}

func (s *State) admitLocked(key string, source netip.Addr, payload bool, now time.Time) bool {
	u := s.users[s.byKey[key]]
	if !validAt(u, now) || !source.IsValid() {
		return false
	}
	raw := source.Unmap().String()
	for ip, last := range u.sources {
		if now.Sub(last) >= sourceTTL {
			delete(u.sources, ip)
		}
	}
	if !payload {
		if _, ok := u.sources[raw]; ok {
			u.sources[raw] = now
			return true
		}
		return false
	}
	counted := s.filter.CountKey(raw)
	if u.spec.DeviceLimit > 0 && counted != "" {
		keys := make(map[string]bool)
		for ip := range u.sources {
			if k := s.filter.CountKey(ip); k != "" {
				keys[k] = true
			}
		}
		if now.Sub(s.globalAt) < globalTTL {
			for _, ip := range s.global[u.spec.ID] {
				if k := s.filter.CountKey(ip); k != "" {
					keys[k] = true
				}
			}
		}
		if !keys[counted] && len(keys) >= u.spec.DeviceLimit {
			if reporter, ok := s.connLimiter.(model.DeviceLimitReporter); ok {
				reporter.ReportDeviceLimited(u.spec.ID, u.spec.DeviceLimit, len(keys), raw)
			}
			return false
		}
	}
	u.sources[raw], u.latest = now, source.Unmap()
	return true
}

func (s *State) transfer(id, direction, size int, expected *userState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[id]
	if !validAt(u, time.Now()) || (expected != nil && expected != u) || u.ctx.Err() != nil {
		return false
	}
	total := s.totals[id]
	total[direction] += int64(size)
	s.totals[id] = total
	return true
}

// 等待只发生在该用户的连接中，不阻塞所有用户共用的 WG 收发队列。
func (s *State) wait(ctx context.Context, id, size int, expected *userState) bool {
	s.mu.Lock()
	u := s.users[id]
	if !validAt(u, time.Now()) || u != expected || ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	uuid, speed := u.spec.UUID, s.speed
	s.mu.Unlock()
	if speed != nil {
		if limiter := speed(uuid); limiter != nil {
			remaining := size
			for remaining > 0 {
				n := remaining
				if limiter.Limit() != rate.Inf && n > limiter.Burst() {
					n = limiter.Burst()
				}
				if n <= 0 || limiter.WaitN(ctx, n) != nil {
					return false
				}
				remaining -= n
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users[id] == u && validAt(u, time.Now()) && ctx.Err() == nil
}

func (s *State) userForAddress(a netip.Addr) (int, netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.byAddress[a.Unmap()]
	u := s.users[id]
	if !validAt(u, time.Now()) {
		return 0, netip.Addr{}
	}
	return id, u.latest
}

func (s *State) track(id int, conn io.Closer) (*userState, func(), bool) {
	s.mu.Lock()
	u := s.users[id]
	if !validAt(u, time.Now()) {
		s.mu.Unlock()
		return nil, nil, false
	}
	if l := s.connLimiter; l != nil {
		if limit, ok := l.MaxConnByUserID(id); ok && len(u.connections) >= limit {
			l.ReportLimited(id, model.ConnLimitKindConcurrent, limit, len(u.connections))
			s.mu.Unlock()
			return nil, nil, false
		}
		if !l.AllowNewConn(id) {
			l.ReportLimited(id, model.ConnLimitKindRate, 0, 0)
			s.mu.Unlock()
			return nil, nil, false
		}
	}
	u.connections[conn] = struct{}{}
	s.mu.Unlock()
	return u, func() { s.mu.Lock(); delete(u.connections, conn); s.mu.Unlock() }, true
}

func (s *State) closeUser(uuid string) {
	s.mu.Lock()
	var conns []io.Closer
	for _, u := range s.users {
		if uuid != "" && u.spec.UUID != uuid {
			continue
		}
		for c := range u.connections {
			conns = append(conns, c)
		}
		u.sources = make(map[string]time.Time)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *State) snapshot() (map[int][2]int64, map[int]map[string]bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	traffic := make(map[int][2]int64, len(s.totals))
	alive := make(map[int]map[string]bool)
	for id, v := range s.totals {
		traffic[id] = v
	}
	count := 0
	now := time.Now()
	for id, u := range s.users {
		count += len(u.connections)
		if !validAt(u, now) {
			continue
		}
		for ip, last := range u.sources {
			if now.Sub(last) >= sourceTTL {
				delete(u.sources, ip)
				continue
			}
			if alive[id] == nil {
				alive[id] = make(map[string]bool)
			}
			alive[id][ip] = true
		}
	}
	return traffic, alive, count
}
