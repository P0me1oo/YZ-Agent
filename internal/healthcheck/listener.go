package healthcheck

import (
	"context"
	"net/netip"
	"os"

	gnet "github.com/shirou/gopsutil/v4/net"
)

// Listener 核对当前进程拥有的 TCP 监听，不发送会触发入站认证错误的空连接。
func Listener(ctx context.Context, address string) (bool, error) {
	return listener(
		ctx,
		address,
		int32(os.Getpid()),
		processConnections,
	)
}

func listener(
	ctx context.Context,
	address string,
	pid int32,
	read func(context.Context, int32) ([]gnet.ConnectionStat, error),
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	wanted, err := netip.ParseAddrPort(address)
	if err != nil {
		return false, err
	}
	connections, err := read(ctx, pid)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, connection := range connections {
		ownedListener := connection.Pid == pid && connection.Status == "LISTEN"
		if !ownedListener || connection.Laddr.Port != uint32(wanted.Port()) {
			continue
		}
		bound, err := netip.ParseAddr(connection.Laddr.IP)
		if err != nil {
			continue
		}
		bound, target := bound.Unmap(), wanted.Addr().Unmap()
		if bound == target || bound.IsUnspecified() && bound.Is4() == target.Is4() {
			return true, nil
		}
		ipv6Wildcard := bound.Is6() && bound.IsUnspecified()
		if !ipv6Wildcard || !target.Is4() {
			continue
		}
		listening, err := dualStackListener(connection)
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err != nil || listening {
			return listening, err
		}
	}
	return false, nil
}

// ListenerNetwork validates UDP socket ownership as well as TCP listeners.
func ListenerNetwork(ctx context.Context, network, address string) (bool, error) {
	if network == "tcp" {
		return Listener(ctx, address)
	}
	return listener(ctx, address, int32(os.Getpid()), func(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
		items, err := processNetworkConnections(ctx, pid, "udp")
		for i := range items {
			items[i].Status = "LISTEN"
		}
		return items, err
	})
}
