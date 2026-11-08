package directwg

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestIPConnectionsRetainEachConnectionSource(t *testing.T) {
	s := newState()
	s.replaceUsers([]model.UserSpec{stateUser(t)})
	core := &Managed{state: s, direct: true}
	var releases []func()
	for _, ip := range []string{"8.8.8.8", "8.8.8.8", "1.1.1.1", "127.0.0.1"} {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		_, release, ok := s.track(1, a, netip.MustParseAddr(ip))
		if !ok {
			t.Fatal("连接未登记")
		}
		releases = append(releases, release)
	}
	s.users[1].latest = netip.MustParseAddr("9.9.9.9")
	counts, err := core.GetIPConnectionSnapshot(context.Background())
	if err != nil || counts[1]["8.8.8.8"] != 2 || counts[1]["1.1.1.1"] != 1 || len(counts[1]) != 2 {
		t.Fatalf("握手地址不能改写旧连接: %v %v", counts, err)
	}
	for _, release := range releases {
		release()
		release()
	}
	counts, _ = core.GetIPConnectionSnapshot(context.Background())
	if counts == nil || len(counts) != 0 {
		t.Fatalf("连接未清空: %v", counts)
	}
}
