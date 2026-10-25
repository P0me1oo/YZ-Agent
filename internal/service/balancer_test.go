package service

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/balancer"
	"github.com/P0me1oo/YZ-Agent/internal/balancerkernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestBalancerRunsWithEmptyUsersAndSurvivesSnapshotUpdates(t *testing.T) {
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := socket.Addr().(*net.TCPAddr).Port
	socket.Close()
	spec := &model.NodeSpec{Protocol: "balancer", ListenIP: "127.0.0.1", ServerPort: port, Network: "tcp",
		Balancer: &balancer.Config{Network: "tcp"}}
	s := newTestService(&fakeKernel{})
	s.kernel = balancerkernel.New()
	s.lastConfig = spec
	t.Cleanup(s.kernel.Stop)
	ctx := context.Background()
	if !s.applyChanges(ctx, true, false) {
		t.Fatalf("调度入口未启动: %v", s.runtimeError)
	}
	check := func() {
		t.Helper()
		if !s.kernel.IsRunning() || s.runtimeError != nil {
			t.Fatal("无用户的调度入口被停止")
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	check()
	if !s.applyUserUpdate(ctx, nil, computeUserHash(nil)) {
		t.Fatal(s.runtimeError)
	}
	check()
	next := *spec
	next.Balancer = balancer.CloneConfig(spec.Balancer)
	next.Balancer.Targets = []balancer.Target{{ID: 1, Address: "127.0.0.1:1", Priority: 1, Online: false}}
	if !s.applyConfigUpdate(ctx, &next, computeConfigHash(&next)) {
		t.Fatal(s.runtimeError)
	}
	check()
}
