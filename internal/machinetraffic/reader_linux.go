//go:build linux

package machinetraffic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

// 默认路由只用来选择计费网卡；不发探测流量，也不修改系统路由。
func defaultInterfaces() ([]string, error) {
	names := map[string]bool{}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteList(nil, family)
		if err != nil {
			return nil, err
		}
		for _, index := range defaultRouteLinkIndices(routes) {
			link, err := netlink.LinkByIndex(index)
			if err != nil {
				return nil, err
			}
			names[link.Attrs().Name] = true
		}
	}
	result := []string{}
	for name := range names {
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, errors.New("no default external interface")
	}
	return result, nil
}

// 统计硬件出口或容器自身的外网 veth；bond/VLAN 的父子接口只计一次。
func billingInterfaces(root string, names []string, linkByName func(string) (netlink.Link, error)) ([]string, error) {
	return selectInterfaces(names, func(name string) (interfaceInfo, error) {
		base := filepath.Join(root, name)
		target, err := filepath.EvalSymlinks(base)
		if err != nil {
			return interfaceInfo{}, err
		}
		lowers, err := filepath.Glob(filepath.Join(base, "lower_*"))
		if err != nil {
			return interfaceInfo{}, err
		}
		info := interfaceInfo{}
		for _, lower := range lowers {
			info.lower = append(info.lower, strings.TrimPrefix(filepath.Base(lower), "lower_"))
		}
		_, bridgeErr := os.Stat(filepath.Join(base, "bridge"))
		info.bridge = bridgeErr == nil
		typeBytes, err := os.ReadFile(filepath.Join(base, "type"))
		if err != nil {
			return interfaceInfo{}, err
		}
		if strings.TrimSpace(string(typeBytes)) != "1" {
			return interfaceInfo{}, errors.New("billing interface is not Ethernet")
		}
		info.billable = !strings.Contains(filepath.ToSlash(target), "/virtual/")
		if !info.billable {
			link, err := linkByName(name)
			if err != nil {
				return interfaceInfo{}, err
			}
			switch link.Type() {
			case "veth":
				// LXC 出口的对端位于其他网络空间。宿主机桥接端口和本地
				// veth 对不能作为出口计量，避免累计容器内部或重复流量。
				attrs := link.Attrs()
				info.billable = attrs.NetNsID >= 0 && attrs.MasterIndex == 0
			case "bond", "vlan", "bridge":
				// 仅这些聚合接口可以继续解析底层网卡；隧道不能借此通过。
			default:
				return interfaceInfo{}, errors.New("unsupported virtual billing interface")
			}
		}
		return info, nil
	})
}

func Read(names []string) (Reading, error) {
	var err error
	if len(names) == 0 {
		names, err = defaultInterfaces()
		if err != nil {
			return Reading{}, err
		}
	}
	names, err = billingInterfaces("/sys/class/net", names, netlink.LinkByName)
	if err != nil {
		return Reading{}, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Reading{}, err
	}
	r := Reading{Boot: strings.TrimSpace(string(boot)), Counters: map[string]Counter{}}
	readNumber := func(path string) (uint64, error) {
		b, e := os.ReadFile(path)
		if e != nil {
			return 0, e
		}
		return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	for _, name := range names {
		base := filepath.Join("/sys/class/net", name)
		index, e := readNumber(filepath.Join(base, "ifindex"))
		if e != nil {
			return Reading{}, e
		}
		rx, e := readNumber(filepath.Join(base, "statistics/rx_bytes"))
		if e != nil {
			return Reading{}, e
		}
		tx, e := readNumber(filepath.Join(base, "statistics/tx_bytes"))
		if e != nil {
			return Reading{}, e
		}
		r.Counters[name] = Counter{Identity: fmt.Sprint(index), RX: rx, TX: tx}
	}
	return r, nil
}
