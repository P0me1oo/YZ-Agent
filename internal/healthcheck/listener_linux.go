//go:build linux

package healthcheck

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"

	gcommon "github.com/shirou/gopsutil/v4/common"
	gnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/unix"
)

func processConnections(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
	return processNetworkConnections(ctx, pid, "tcp")
}
func processNetworkConnections(ctx context.Context, pid int32, network string) ([]gnet.ConnectionStat, error) {
	// 外部主机监控的 HOST_PROC 不能改变实际运行进程的监听归属。
	ctx = context.WithValue(ctx, gcommon.EnvKey, gcommon.EnvMap{gcommon.HostProcEnvKey: "/proc"})
	// WireGuard 空间创建可能结束主线程；其连接表不能代表当前监听空间。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	readPID := pid
	if pid == int32(os.Getpid()) {
		readPID = int32(unix.Gettid())
	}
	fd, err := os.Open("/proc/" + strconv.Itoa(int(readPID)) + "/fd")
	if err != nil {
		return nil, err
	}
	fd.Close()
	connections, err := gnet.ConnectionsPidWithoutUidsWithContext(ctx, network, readPID)
	if err != nil {
		return nil, err
	}
	// 同一线程组共用描述符，仍向调用方提供进程编号而非线程编号。
	for index := range connections {
		if connections[index].Pid == readPID {
			connections[index].Pid = pid
		}
	}
	return connections, nil
}

func dualStackListener(connection gnet.ConnectionStat) (bool, error) {
	if connection.Pid != int32(os.Getpid()) {
		return false, nil
	}
	// 复制描述符后检查，避免重载期间关闭、复用原编号影响后续读取。
	fd, err := unix.FcntlInt(uintptr(connection.Fd), unix.F_DUPFD_CLOEXEC, 0)
	if errors.Is(err, unix.EBADF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)

	address, err := unix.Getsockname(fd)
	if errors.Is(err, unix.ENOTSOCK) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	bound, ok := address.(*unix.SockaddrInet6)
	if !ok || bound.Port != int(connection.Laddr.Port) {
		return false, nil
	}
	if bound.Addr != [16]byte{} {
		return false, nil
	}
	// 描述符可能已在复制前复用，重新确认地址、端口、类型和监听状态。
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return false, err
	}
	if kind != unix.SOCK_STREAM {
		return false, nil
	}
	accepting, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		return false, err
	}
	if accepting != 1 {
		return false, nil
	}
	// IPv6 通配监听只有明确关闭 IPV6_V6ONLY 时才覆盖 IPv4。
	v6only, err := unix.GetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY)
	if err != nil {
		return false, err
	}
	return v6only == 0, nil
}
