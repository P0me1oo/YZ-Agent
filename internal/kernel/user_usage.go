package kernel

import (
	"context"
	"sort"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// UsageReader collects billing counters once and copies addresses only for requested users.
// The sample belongs to the caller and remains valid across kernel reconfiguration.
type UsageReader interface {
	ReadUserUsage(context.Context, []int) UsageSample
}

type UsageSample struct {
	Traffic     model.TrafficCounters
	OnlineUsers int
	Connections int
	Users       map[int]*UserConnections
}

type UserConnections struct {
	Count     int
	addresses map[string]struct{}
	overflow  bool
}

func NewUsageSample(ids []int) UsageSample {
	var sample UsageSample
	if len(ids) != 0 {
		sample.Users = make(map[int]*UserConnections, len(ids))
		for _, id := range ids {
			sample.Users[id] = &UserConnections{}
		}
	}
	return sample
}

// A very large user's list must not exhaust the shared control frame or look complete.
func (u *UserConnections) AddAddress(source string) {
	if u.overflow {
		return
	}
	ip := deviceip.PublicAddress(source)
	if ip == "" {
		return
	}
	if u.addresses == nil {
		u.addresses = make(map[string]struct{})
	}
	u.addresses[ip] = struct{}{}
	if len(u.addresses) > 256 {
		u.overflow = true
		u.addresses = nil
	}
}

func (u *UserConnections) Addresses() []string {
	if u.overflow {
		return nil
	}
	ips := make([]string, 0, len(u.addresses))
	for ip := range u.addresses {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips
}
