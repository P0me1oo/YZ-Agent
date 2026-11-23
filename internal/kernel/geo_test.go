package kernel

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestBlockRoutesNeedIP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		route model.RouteRule
		want  bool
	}{
		{"category", model.RouteRule{Action: "block", Match: []string{" geoip:cn "}}, true},
		{"IPv4", model.RouteRule{Action: "block", Match: []string{"192.0.2.1"}}, true},
		{"IPv6", model.RouteRule{Action: "block", Match: []string{"2001:db8::1"}}, true},
		{"CIDR", model.RouteRule{Action: "block", Match: []string{"192.0.2.0/24"}}, true},
		{"domain", model.RouteRule{Action: "block", Match: []string{"geosite:openai"}}, false},
		{"BT", model.RouteRule{Action: "block", Protocols: []string{"bittorrent"}}, false},
		{"direct", model.RouteRule{Action: "direct", Match: []string{"geoip:cn"}}, false},
		{"invalid", model.RouteRule{Action: "block", Match: []string{"geoip:cn"}, Ports: []string{"0"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BlockRoutesNeedIP([]model.RouteRule{tc.route}); got != tc.want {
				t.Fatalf("need IP = %v, want %v", got, tc.want)
			}
		})
	}
}
