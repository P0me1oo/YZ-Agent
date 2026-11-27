//go:build !linux

package monitor

import (
	stdnet "net"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

func readMemory() (total, used uint64, ok bool) {
	v, err := mem.VirtualMemory()
	if err != nil || v.Total == 0 || v.Available > v.Total {
		return 0, 0, false
	}
	return v.Total, v.Total - v.Available, true
}

func readLinks() map[string]linkCounter {
	values, err := net.IOCounters(true)
	if err != nil {
		return nil
	}
	counters := make(map[string]linkCounter)
	for _, v := range values {
		if skipInterface(v.Name) {
			continue
		}
		link, err := stdnet.InterfaceByName(v.Name)
		if err != nil {
			return nil
		}
		if link.Flags&stdnet.FlagLoopback == 0 {
			counters[v.Name] = linkCounter{index: link.Index, rx: v.BytesRecv, tx: v.BytesSent}
		}
	}
	return counters
}

func skipInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range []string{"docker", "veth", "br-", "virbr", "vnet", "tun", "tap", "wg"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
