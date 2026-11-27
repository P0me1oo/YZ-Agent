//go:build linux

package monitor

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

func readMemory() (total, used uint64, ok bool) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	return memoryUsage(bufio.NewScanner(file))
}

func memoryUsage(lines *bufio.Scanner) (total, used uint64, ok bool) {
	var available uint64
	var haveTotal, haveAvailable bool
	for lines.Scan() {
		key, value, _ := strings.Cut(lines.Text(), ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		number, kb := strings.CutSuffix(strings.TrimSpace(value), " kB")
		n, err := strconv.ParseUint(number, 10, 64)
		if !kb || err != nil || n > math.MaxUint64/1024 {
			return 0, 0, false
		}
		if key == "MemTotal" {
			total, haveTotal = n*1024, true
		} else {
			available, haveAvailable = n*1024, true
		}
		if haveTotal && haveAvailable {
			if total == 0 || available > total {
				return 0, 0, false
			}
			return total, total - available, true
		}
	}
	return 0, 0, false
}

func readLinks() map[string]linkCounter {
	const base = "/sys/class/net"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	counters := make(map[string]linkCounter)
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(base, name)
		target, err := os.Readlink(path)
		if err != nil {
			return nil
		}
		if strings.Contains(target, "/virtual/") {
			continue
		}
		counter, ok := readPhysicalLink(path)
		if !ok {
			return nil
		}
		counters[name] = counter
	}
	if len(counters) != 0 {
		// Bond, bridge and VLAN masters repeat their physical slaves' traffic.
		return counters
	}
	// A container may see only virtual devices. Include its unbridged veth
	// uplinks with a peer in another namespace, whatever their local names.
	links, err := netlink.LinkList()
	if err != nil {
		return nil
	}
	for _, link := range links {
		attrs := link.Attrs()
		if link.Type() != "veth" || attrs.NetNsID < 0 || attrs.MasterIndex != 0 {
			continue
		}
		if attrs.Statistics == nil {
			return nil
		}
		counters[attrs.Name] = linkCounter{index: attrs.Index, rx: attrs.Statistics.RxBytes, tx: attrs.Statistics.TxBytes}
	}
	return counters
}

func readPhysicalLink(path string) (linkCounter, bool) {
	// Hold the sysfs directory so all fields refer to the same device if the
	// interface is removed and a new device takes over its name mid-sample.
	root, err := os.OpenRoot(path)
	if err != nil {
		return linkCounter{}, false
	}
	defer root.Close()
	read := func(name string) (uint64, bool) {
		file, err := root.Open(name)
		if err != nil {
			return 0, false
		}
		defer file.Close()
		var raw [32]byte
		n, err := file.Read(raw[:])
		if err != nil || n == 0 || raw[n-1] != '\n' {
			return 0, false
		}
		number, err := strconv.ParseUint(strings.TrimSpace(string(raw[:n])), 10, 64)
		return number, err == nil
	}
	index, iok := read("ifindex")
	rx, rok := read("statistics/rx_bytes")
	tx, tok := read("statistics/tx_bytes")
	return linkCounter{index: int(index), rx: rx, tx: tx}, iok && rok && tok
}
