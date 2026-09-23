package deviceip

import "testing"

func TestPublic(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true}, {"2001:4860:4860::8888", true},
		{"10.0.0.2", false}, {"172.16.0.1", false}, {"192.168.1.1", false},
		{"127.0.0.1", false}, {"::1", false}, {"fe80::1", false},
		{"100.64.0.1", false}, {"192.0.2.1", false}, {"2001:db8::1", false},
		{"::ffff:8.8.8.8", true}, {"::ffff:10.0.0.1", false},
		{"invalid", false},
	} {
		if got := Public(tc.ip); got != tc.want {
			t.Errorf("Public(%q) = %t, want %t", tc.ip, got, tc.want)
		}
	}
	if got := Normalize("::ffff:8.8.8.8"); got != "8.8.8.8" {
		t.Errorf("映射地址应与 IPv4 合并，得到 %q", got)
	}
}
