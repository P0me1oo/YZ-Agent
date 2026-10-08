package directwg

import (
	"errors"
	"io"
	"net/netip"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func (s *State) pruneSourcesLocked(u *userState, now time.Time) {
	for ip, last := range u.sources {
		if now.Sub(last) < sourceTTL {
			continue
		}
		delete(u.sources, ip)
		if p := u.sourcePermits[ip]; p != nil {
			delete(u.sourcePermits, ip)
			go s.closeDeviceSource(u, ip, p)
		}
	}
}

// 首个已认证数据包只触发异步准入，不能在 WG 共用收包队列内等待面板或其它节点。
func (s *State) admitManagedSourceLocked(u *userState, raw string, payload bool, now time.Time) bool {
	if p := u.sourcePermits[raw]; p != nil {
		if !p.Valid() {
			return false
		}
		u.sources[raw] = now
		if payload {
			u.latest = netip.MustParseAddr(raw)
		}
		return true
	}
	if !payload || u.pendingSources[raw] || now.Before(u.retrySources[raw]) || len(u.pendingSources) >= 32 {
		return false
	}
	if u.pendingSources == nil {
		u.pendingSources = make(map[string]bool)
	}
	u.pendingSources[raw] = true
	gate, id, ctx := s.deviceGate, u.spec.ID, u.ctx
	go func() {
		permit, err := gate.Acquire(ctx, id, raw)
		s.mu.Lock()
		delete(u.pendingSources, raw)
		valid := err == nil && permit != nil && permit.Valid() && s.users[id] == u && validAt(u, time.Now()) && ctx.Err() == nil
		if valid {
			if u.sourcePermits == nil {
				u.sourcePermits = make(map[string]*devicegate.Permit)
			}
			u.sourcePermits[raw] = permit
			u.sources[raw], u.latest = time.Now(), netip.MustParseAddr(raw)
		} else {
			if u.retrySources == nil {
				u.retrySources = make(map[string]time.Time)
			}
			for ip, until := range u.retrySources {
				if time.Now().After(until) {
					delete(u.retrySources, ip)
				}
			}
			u.retrySources[raw] = time.Now().Add(time.Second)
		}
		limiter := s.connLimiter
		s.mu.Unlock()
		if !valid {
			permit.Release()
			var denied *devicegate.DeniedError
			if errors.As(err, &denied) {
				if reporter, ok := limiter.(model.DeviceLimitReporter); ok {
					reporter.ReportDeviceLimited(id, denied.Limit, denied.Observed, raw)
				}
			}
			return
		}
		permit.Bind(func() { s.closeDeviceSource(u, raw, permit) })
	}()
	return false
}

func (s *State) closeDeviceSource(u *userState, raw string, permit *devicegate.Permit) {
	s.mu.Lock()
	if u.sourcePermits[raw] == permit {
		delete(u.sourcePermits, raw)
		delete(u.sources, raw)
	}
	var connections []io.Closer
	for conn, owner := range u.connectionSources {
		if owner == permit {
			connections = append(connections, conn)
		}
	}
	s.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	s.mu.Lock()
	for _, conn := range connections {
		if u.connectionSources[conn] == permit {
			delete(u.connectionSources, conn)
			delete(u.connections, conn)
		}
	}
	s.mu.Unlock()
	permit.Release()
}
