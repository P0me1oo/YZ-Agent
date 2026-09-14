package xray

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/protocol"
	xrayCore "github.com/xtls/xray-core/core"
	featurebandwidth "github.com/xtls/xray-core/features/bandwidth"
	xraystats "github.com/xtls/xray-core/features/stats"
	"golang.org/x/time/rate"
)

type recordingUserManager struct {
	users     map[string]*protocol.MemoryUser
	calls     []string
	addErr    error
	removeErr error
}

func newRecordingUserManager(users ...*protocol.MemoryUser) *recordingUserManager {
	manager := &recordingUserManager{users: make(map[string]*protocol.MemoryUser, len(users))}
	for _, user := range users {
		manager.users[user.Email] = user
	}
	return manager
}

func (m *recordingUserManager) AddUser(_ context.Context, user *protocol.MemoryUser) error {
	m.calls = append(m.calls, "add:"+user.Email)
	if m.addErr != nil {
		return m.addErr
	}
	if _, exists := m.users[user.Email]; exists {
		return fmt.Errorf("duplicate user %s", user.Email)
	}
	m.users[user.Email] = user
	return nil
}

func (m *recordingUserManager) RemoveUser(_ context.Context, email string) error {
	m.calls = append(m.calls, "remove:"+email)
	if m.removeErr != nil {
		return m.removeErr
	}
	if _, exists := m.users[email]; !exists {
		return fmt.Errorf("unknown user %s", email)
	}
	delete(m.users, email)
	return nil
}

func (m *recordingUserManager) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	return m.users[email]
}

func (m *recordingUserManager) GetUsers(_ context.Context) []*protocol.MemoryUser {
	users := make([]*protocol.MemoryUser, 0, len(m.users))
	for _, user := range m.users {
		users = append(users, user)
	}
	return users
}

func (m *recordingUserManager) GetUsersCount(_ context.Context) int64 {
	return int64(len(m.users))
}

func TestUserManagerUUIDReplacementRemovesBeforeAdd(t *testing.T) {
	oldUser := model.UserSpec{ID: 15, UUID: "11111111-1111-4111-8111-111111111111"}
	newUser := model.UserSpec{ID: 15, UUID: "22222222-2222-4222-8222-222222222222"}
	manager := newRecordingUserManager(&protocol.MemoryUser{Email: userEmail(oldUser.ID)})

	toAdd, toRemove := kernel.UserDiff([]model.UserSpec{oldUser}, []model.UserSpec{newUser})
	removed, err := removeUsersFromManager(context.Background(), manager, toRemove)
	if err != nil {
		t.Fatalf("removeUsersFromManager() error = %v", err)
	}
	added, err := addUsersToManager(
		context.Background(),
		manager,
		"vless",
		&model.NodeSpec{Protocol: "vless"},
		toAdd,
	)
	if err != nil {
		t.Fatalf("addUsersToManager() error = %v", err)
	}

	if removed != 1 || added != 1 {
		t.Fatalf("replacement counts = (+%d -%d), want (+1 -1)", added, removed)
	}
	wantCalls := []string{"remove:user@15", "add:user@15"}
	if fmt.Sprint(manager.calls) != fmt.Sprint(wantCalls) {
		t.Fatalf("manager calls = %v, want %v", manager.calls, wantCalls)
	}
	if manager.GetUsersCount(context.Background()) != 1 {
		t.Fatalf("manager user count = %d, want 1", manager.GetUsersCount(context.Background()))
	}
}

func TestUserManagerUpdateFailureIsReturned(t *testing.T) {
	addErr := errors.New("add rejected")
	manager := newRecordingUserManager()
	manager.addErr = addErr

	added, err := addUsersToManager(
		context.Background(),
		manager,
		"vless",
		&model.NodeSpec{Protocol: "vless"},
		[]model.UserSpec{{ID: 15, UUID: "22222222-2222-4222-8222-222222222222"}},
	)

	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if !errors.Is(err, addErr) {
		t.Fatalf("addUsersToManager() error = %v, want wrapped %v", err, addErr)
	}
}

func TestUserManagerRemovalFailureIsReturned(t *testing.T) {
	removeErr := errors.New("remove rejected")
	manager := newRecordingUserManager(&protocol.MemoryUser{Email: userEmail(15)})
	manager.removeErr = removeErr

	removed, err := removeUsersFromManager(
		context.Background(),
		manager,
		[]model.UserSpec{{ID: 15, UUID: "11111111-1111-4111-8111-111111111111"}},
	)

	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("removeUsersFromManager() error = %v, want wrapped %v", err, removeErr)
	}
}

func newStatsManager(t *testing.T) xraystats.Manager {
	t.Helper()
	mgr, err := appstats.NewManager(context.Background(), &appstats.Config{})
	if err != nil {
		t.Fatalf("stats.NewManager() error = %v", err)
	}
	return mgr
}

func mustRegisterCounter(t *testing.T, mgr xraystats.Manager, name string) xraystats.Counter {
	t.Helper()
	counter, err := mgr.RegisterCounter(name)
	if err != nil {
		t.Fatalf("RegisterCounter(%q) error = %v", name, err)
	}
	return counter
}

func counterName(userID int, direction string) string {
	return fmt.Sprintf("user>>>%s>>>traffic>>>%s", userEmail(userID), direction)
}

func newStatsBackedXray(t *testing.T, users []model.UserSpec, ld *LimitDispatcher) (*Xray, xraystats.Manager) {
	t.Helper()
	mgr := newStatsManager(t)
	inst := new(xrayCore.Instance)
	if err := inst.AddFeature(mgr); err != nil {
		t.Fatalf("Instance.AddFeature(stats) error = %v", err)
	}
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = inst
	x.limitDispatcher = ld
	x.users = users
	x.cumTraffic = make(map[int][2]int64)
	x.running.Store(true)
	return x, mgr
}

func TestXrayGetUserTrafficUsesBuiltInStatsAndDispatcherState(t *testing.T) {
	users := []model.UserSpec{
		{ID: 1, UUID: "uuid-1"},
		{ID: 2, UUID: "uuid-2"},
	}

	ld := newTestDispatcher()
	email1 := userEmail(1)
	email2 := userEmail(2)
	ld.UpdateLimits(map[string]int{email1: 1, email2: 2}, nil, nil)
	ld.mu.Lock()
	ld.limitedIPs[email1] = map[string]int{"1.1.1.1": 1}
	ld.mu.Unlock()
	ref := &atomic.Int64{}
	ref.Store(1)
	ic := &ipCounter{}
	ic.ips.Store("2.2.2.2", ref)
	ld.unlimitedIPs.Store(email2, ic)
	ld.connCount.Store(7)

	x, mgr := newStatsBackedXray(t, users, ld)
	up1 := mustRegisterCounter(t, mgr, counterName(1, "uplink"))
	down1 := mustRegisterCounter(t, mgr, counterName(1, "downlink"))
	up2 := mustRegisterCounter(t, mgr, counterName(2, "uplink"))
	down2 := mustRegisterCounter(t, mgr, counterName(2, "downlink"))
	up1.Add(100)
	down1.Add(200)
	up2.Add(50)
	down2.Add(80)

	traffic, aliveIPs, connCount, err := x.GetUserTraffic(context.Background())
	if err != nil {
		t.Fatalf("GetUserTraffic() error = %v", err)
	}
	if connCount != 7 {
		t.Fatalf("connCount = %d, want 7", connCount)
	}
	if got := traffic[1]; got != [2]int64{100, 200} {
		t.Fatalf("traffic[1] = %v, want [100 200]", got)
	}
	if got := traffic[2]; got != [2]int64{50, 80} {
		t.Fatalf("traffic[2] = %v, want [50 80]", got)
	}
	if !aliveIPs[1]["1.1.1.1"] {
		t.Fatal("expected limited-user IP to be reported")
	}
	if !aliveIPs[2]["2.2.2.2"] {
		t.Fatal("expected unlimited-user IP to be reported")
	}

	up1.Add(30)
	down1.Add(40)
	traffic, _, _, err = x.GetUserTraffic(context.Background())
	if err != nil {
		t.Fatalf("second GetUserTraffic() error = %v", err)
	}
	if got := traffic[1]; got != [2]int64{130, 240} {
		t.Fatalf("traffic[1] after second poll = %v, want [130 240]", got)
	}
	if got := traffic[2]; got != [2]int64{50, 80} {
		t.Fatalf("traffic[2] after second poll = %v, want [50 80]", got)
	}
}

func TestXrayUpdateDispatcherLimitsPropagatesDeviceMetadata(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	ld := newTestDispatcher()
	x.limitDispatcher = ld

	users := []model.UserSpec{
		{ID: 7, UUID: "uuid-7", DeviceLimit: 2, SpeedLimit: 12},
		{ID: 9, UUID: "uuid-9", DeviceLimit: 0, SpeedLimit: 0},
	}

	x.updateDispatcherLimits(users)

	if got := ld.deviceLimits[userEmail(7)]; got != 2 {
		t.Fatalf("deviceLimits[email] = %d, want 2", got)
	}
	if got := ld.deviceLimits["uuid-7"]; got != 2 {
		t.Fatalf("deviceLimits[uuid] = %d, want 2", got)
	}
	if _, ok := ld.deviceLimits[userEmail(9)]; ok {
		t.Fatal("unexpected device limit entry for unlimited user")
	}
}

func TestXraySetSpeedLimitFuncUsesPatchedCorePath(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	called := false

	x.SetSpeedLimitFunc(func(string) *rate.Limiter {
		called = true
		return rate.NewLimiter(rate.Limit(1), 1)
	})

	if called {
		t.Fatal("patched xray path should not consult external speed-limit callbacks")
	}

	mu, err := toMemoryUser("vless", &model.NodeSpec{Protocol: "vless"}, model.UserSpec{ID: 1, UUID: "11111111-1111-1111-1111-111111111111", SpeedLimit: 8})
	if err != nil {
		t.Fatalf("toMemoryUser() error = %v", err)
	}
	if mu.Level != 0 {
		t.Fatalf("MemoryUser.Level = %d, want 0", mu.Level)
	}
}


func TestXrayCapabilities(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	caps := x.Capabilities()
	if !caps.PerUserSpeedLimit || !caps.DeviceLimit || !caps.BuiltInTrafficStats || !caps.AliveIPTracking {
		t.Fatalf("unexpected positive xray capabilities: %+v", caps)
	}
	if caps.ForceCloseConnection || caps.ForceCloseUser {
		t.Fatalf("unexpected force-close xray capabilities: %+v", caps)
	}
}


func TestXrayUpdateBandwidthLimitsWritesPatchedCoreFeature(t *testing.T) {
	inst := new(xrayCore.Instance)
	bm := featurebandwidth.New()
	if err := inst.AddFeature(bm); err != nil {
		t.Fatalf("Instance.AddFeature(bandwidth) error = %v", err)
	}
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = inst
	x.updateBandwidthLimits([]model.UserSpec{{ID: 1, UUID: "uuid-1", SpeedLimit: 8}})
	lim := bm.GetUserLimiter(userEmail(1))
	if lim == nil {
		t.Fatal("expected patched bandwidth feature to receive user limiter")
	}
}


func TestXrayUpdateBandwidthLimitsUsesSpeedLimitFunc(t *testing.T) {
	inst := new(xrayCore.Instance)
	bm := featurebandwidth.New()
	if err := inst.AddFeature(bm); err != nil {
		t.Fatalf("Instance.AddFeature(bandwidth) error = %v", err)
	}
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = inst
	x.users = []model.UserSpec{{ID: 1, UUID: "uuid-1", SpeedLimit: 8}}
	shared := rate.NewLimiter(7, 7)
	x.SetSpeedLimitFunc(func(uuid string) *rate.Limiter {
		if uuid != "uuid-1" {
			t.Fatalf("unexpected uuid: %s", uuid)
		}
		return shared
	})
	lim := bm.GetUserLimiter(userEmail(1))
	if lim != shared {
		t.Fatal("expected patched bandwidth feature to consume shared limiter from callback")
	}
}


func TestXrayUpdateBandwidthLimitsFallsBackToUserSpeed(t *testing.T) {
	inst := new(xrayCore.Instance)
	bm := featurebandwidth.New()
	if err := inst.AddFeature(bm); err != nil {
		t.Fatalf("Instance.AddFeature(bandwidth) error = %v", err)
	}
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = inst
	x.users = []model.UserSpec{{ID: 2, UUID: "uuid-2", SpeedLimit: 16}}
	x.updateBandwidthLimits(nil)
	if bm.GetUserLimiter(userEmail(2)) == nil {
		t.Fatal("expected fallback limiter derived from user speed")
	}
}


func TestXrayUpdateUsersLimitOnlyRefreshesDispatcherAndBandwidth(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	ld := newTestDispatcher()
	inst := new(xrayCore.Instance)
	bm := featurebandwidth.New()
	if err := inst.AddFeature(bm); err != nil {
		t.Fatalf("Instance.AddFeature(bandwidth) error = %v", err)
	}
	x.instance = inst
	x.limitDispatcher = ld
	x.users = []model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 1, SpeedLimit: 8}}

	added, removed, err := x.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 3, SpeedLimit: 16}})
	if err != nil {
		t.Fatalf("UpdateUsers() error = %v", err)
	}
	if added != 0 || removed != 0 {
		t.Fatalf("UpdateUsers() counts = (%d, %d), want (0, 0)", added, removed)
	}
	if got := ld.deviceLimits[userEmail(1)]; got != 3 {
		t.Fatalf("device limit after refresh = %d, want 3", got)
	}
	if bm.GetUserLimiter(userEmail(1)) == nil {
		t.Fatal("expected bandwidth limiter to be refreshed for unchanged user set")
	}
}

func TestXrayRemoveUsersStopsKernelWhenLastUserRemoved(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = new(xrayCore.Instance)
	ld := newTestDispatcher()
	x.limitDispatcher = ld
	x.users = []model.UserSpec{{ID: 1, UUID: "uuid-1"}}
	x.running.Store(true)

	removed, err := x.RemoveUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1"}})
	if err != nil {
		t.Fatalf("RemoveUsers() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if x.IsRunning() {
		t.Fatal("expected xray to stop when last user is removed")
	}
}
