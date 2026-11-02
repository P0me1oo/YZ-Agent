package devicegate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
)

const leaseGrace = time.Hour

// 使用请求发出时间计算本地单调时钟期限，响应传输时间不能增加宽限期。
func leaseDeadline(item Lease, serverTime int64, started time.Time) (time.Time, error) {
	if item.UserID <= 0 || deviceip.PublicAddress(item.IP) != item.IP || !validLease(item.Lease) ||
		item.ConfirmedAt <= 0 || serverTime < item.ConfirmedAt || item.ExpiresAt <= item.ConfirmedAt ||
		item.ExpiresAt-item.ConfirmedAt > leaseGrace.Milliseconds() {
		return time.Time{}, fmt.Errorf("设备许可期限无效")
	}
	remaining := item.ExpiresAt - serverTime
	if remaining <= 0 {
		return time.Time{}, ErrRevoked
	}
	deadline := started.Add(time.Duration(remaining) * time.Millisecond)
	if !time.Now().Before(deadline) {
		return time.Time{}, ErrRevoked
	}
	return deadline, nil
}

func (m *Manager) retireLocked(item Revocation) {
	if item.Run == "" {
		if previous, exists := m.retired[item.Lease]; exists {
			// 无本地来源的重复撤销不能覆盖尚待确认的原会话身份。
			item = previous.item
		} else {
			item.Run = m.run
		}
	}
	m.seq++
	m.retired[item.Lease] = retiredSource{item: item, sequence: m.seq}
}

func (m *Manager) removeSourceLocked(source *sourceState) {
	if source.expiry != nil {
		source.expiry.Stop()
	}
	delete(m.sources, source.key)
	m.retireLocked(Revocation{UserID: source.key.user, IP: source.key.ip, Lease: source.lease,
		Run: source.run, ConfirmedSequence: source.leaseSequence})
}

func (m *Manager) installLeaseLocked(item Lease, deadline time.Time, sequence uint64) bool {
	if m.closing || !m.ready || (m.users != nil && !m.users[item.UserID]) {
		return false
	}
	if sequence <= m.permissionBarriers[item.UserID] {
		return false
	}
	if revoked, exists := m.revoked[item.Lease]; exists {
		// 既要在撤销后发起，也要由面板确认本次请求；旧主控或旧许可回包不能解除撤销。
		if sequence <= revoked.barrier || item.ConfirmedSequence < sequence {
			return false
		}
	}
	key := sourceKey{user: item.UserID, ip: item.IP}
	source := m.sources[key]
	if source != nil {
		if source.confirmedSequence > sequence || source.revoked {
			return false
		}
		if source.lease != item.Lease {
			if len(source.permits) > 0 {
				return false
			}
			m.removeSourceLocked(source)
			source = nil
		} else if item.ConfirmedAt <= source.confirmedAt {
			// 重传/乱序结果不重建定时器，也不能移动同一次确认的期限。
			source.confirmedSequence = sequence
			source.leaseSequence = max(source.leaseSequence, item.ConfirmedSequence)
			source.omissionSequence = max(source.omissionSequence, sequence)
			return time.Now().Before(source.deadline)
		}
	}
	if source == nil {
		source = &sourceState{key: key, run: m.run, lease: item.Lease, permits: make(map[*Permit]struct{})}
		m.sources[key] = source
	}
	source.confirmedAt, source.expiresAt, source.confirmedSequence = item.ConfirmedAt, item.ExpiresAt, sequence
	source.leaseSequence = item.ConfirmedSequence
	source.omissionSequence = sequence
	source.deadline = deadline
	delete(m.retired, item.Lease)
	if source.expiry != nil {
		source.expiry.Stop()
	}
	source.expiry = time.AfterFunc(time.Until(deadline), func() { m.expireSource(source, deadline) })
	return true
}

// 到期关闭与面板同步分开运行，网络调用阻塞不能推迟关闭。
func (m *Manager) expireSource(source *sourceState, deadline time.Time) {
	var permits []*Permit
	m.mu.Lock()
	if m.sources[source.key] == source && source.deadline.Equal(deadline) && !time.Now().Before(deadline) {
		for permit := range source.permits {
			permits = append(permits, permit)
		}
		if len(source.permits) == 0 {
			m.removeSourceLocked(source)
		}
	}
	m.mu.Unlock()
	for _, permit := range permits {
		// 各条连接独立关闭，某个底层关闭回调阻塞不能拖住其他到期连接。
		go permit.revoke()
	}
}

// SetUsers 与面板下发的账号/节点权限同步，已知移除立即使缓存和连接失效。
func (m *Manager) SetUsers(ids []int) {
	if m == nil {
		return
	}
	users := make(map[int]bool, len(ids))
	for _, id := range ids {
		users[id] = true
	}
	var revoked []Revocation
	m.mu.Lock()
	removed := make(map[int]bool)
	for id := range m.users {
		if !users[id] {
			removed[id] = true
		}
	}
	for key := range m.flights {
		if !users[key.user] {
			removed[key.user] = true
		}
	}
	m.users = users
	for _, source := range m.sources {
		if !users[source.key.user] {
			removed[source.key.user] = true
			revoked = append(revoked, Revocation{UserID: source.key.user, IP: source.key.ip, Lease: source.lease})
		}
	}
	if len(removed) > 0 {
		m.seq++
		if m.permissionBarriers == nil {
			m.permissionBarriers = make(map[int]uint64)
		}
		for id := range removed {
			m.permissionBarriers[id] = m.seq
		}
	}
	permits := m.revokeLocked(revoked, 0)
	m.mu.Unlock()
	revokePermits(permits)
}

func (m *Manager) acceptLeases(reply SyncReply, sequence uint64, started time.Time) error {
	// 撤销优先，即使同一响应含有矛盾的续期内容也不能复活。
	m.applyRevocations(reply.Revoked, sequence, reply.Run)
	deadlines := make([]time.Time, len(reply.Leases))
	present := make(map[string]Lease, len(reply.Leases))
	for i, item := range reply.Leases {
		deadline, err := leaseDeadline(item, reply.ServerTime, started)
		if err != nil {
			return err
		}
		deadlines[i], present[item.Lease] = deadline, item
	}
	var omitted []Revocation
	m.mu.Lock()
	if m.run != reply.Run {
		m.mu.Unlock()
		return ErrNotReady
	}
	for _, source := range m.sources {
		_, found := present[source.lease]
		if source.omissionSequence <= sequence && !found {
			omitted = append(omitted, Revocation{UserID: source.key.user, IP: source.key.ip, Lease: source.lease})
		}
	}
	for i, item := range reply.Leases {
		m.installLeaseLocked(item, deadlines[i], sequence)
	}
	// 缺失许可与安装同属一次状态更新，不能在解锁后撤销刚完成的新核验。
	permits := m.revokeLocked(omitted, sequence)
	// 空闲归还是自愿停用，与强制撤销分开。快照发出后若已建立连接
	// 或正在申请，保留原许可并由下一次活动快照续期。
	var idle []Revocation
	for _, item := range reply.Idle {
		key := sourceKey{user: item.UserID, ip: item.IP}
		source := m.sources[key]
		lease, found := present[item.Lease]
		if !found || lease.UserID != item.UserID || lease.IP != item.IP || m.flights[key] != nil {
			continue
		}
		if source == nil {
			// 在途核验的回包可能丢失。确认当前节点没有这份许可且没有申请，
			// 用本次收到的身份归还，不重建许可，也不沿用旧会话的编号。
			item.Run, item.ConfirmedSequence = m.run, lease.ConfirmedSequence
			idle = append(idle, item)
		} else if source.lease == item.Lease && len(source.permits) == 0 &&
			source.confirmedSequence <= sequence && source.omissionSequence <= sequence {
			idle = append(idle, item)
		}
	}
	// 先建立撤销屏障，再移除缓存；迟到的旧许可不能复活已归还状态。
	permits = append(permits, m.revokeLocked(idle, sequence)...)
	m.mu.Unlock()
	revokePermits(permits)
	return nil
}

func (m *Manager) requestCached(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
	request.LeaseCache = 1
	for {
		if err := ctx.Err(); err != nil {
			return AdmissionReply{}, err
		}
		// 各来源独立申请，慢请求不能占住周期同步或其他账号的等待预算。
		reply, err := m.cachedAttempt(ctx, request)
		if err != nil || reply.Status != "waiting" {
			return reply, err
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AdmissionReply{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) cachedAttempt(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
	snapshot := m.Snapshot()
	snapshot.LeaseCache = 0 // 嵌套快照沿用原格式；能力标记位于请求外层。
	request.Snapshot = &snapshot
	started := time.Now()
	reply, err := m.remote.AdmitDeviceSource(ctx, request)
	if errors.Is(err, ErrSessionLost) {
		m.loseSession(request.Run)
	}
	if err != nil {
		return reply, err
	}
	if err := ctx.Err(); err != nil {
		return AdmissionReply{}, err
	}
	if reply.Sync == nil || reply.Sync.Run != snapshot.Run || reply.Sync.Sequence != snapshot.Sequence {
		return AdmissionReply{}, fmt.Errorf("设备许可申请未被核验")
	}
	m.applyRevocations(append(reply.Sync.Revoked, reply.Revoked...), snapshot.Sequence, snapshot.Run)
	m.confirmSnapshot(snapshot, request.UserID)
	switch reply.Status {
	case "allowed":
		item := Lease{UserID: request.UserID, IP: request.IP, Lease: reply.Lease, ConfirmedAt: reply.ConfirmedAt,
			ExpiresAt: reply.ExpiresAt, ConfirmedSequence: reply.ConfirmedSequence}
		deadline, err := leaseDeadline(item, reply.ServerTime, started)
		if err != nil {
			return AdmissionReply{}, err
		}
		m.mu.Lock()
		// 安装发生在当前所有已采样快照之后；在途的旧空列表不能删除刚授予的许可。
		installed := m.run == request.Run && m.installLeaseLocked(item, deadline, snapshot.Sequence)
		if installed {
			m.seq++
			m.sources[sourceKey{user: item.UserID, ip: item.IP}].omissionSequence = m.seq
		}
		m.mu.Unlock()
		if !installed {
			return AdmissionReply{}, ErrRevoked
		}
		return reply, nil
	case "denied":
		return reply, &DeniedError{Limit: reply.Limit, Observed: reply.Observed, Reason: reply.Reason, Snapshot: reply.LimitSnapshot}
	case "waiting":
		return reply, nil
	default:
		return AdmissionReply{}, fmt.Errorf("设备来源响应状态无效")
	}
}
