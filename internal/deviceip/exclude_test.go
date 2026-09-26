package deviceip

import (
	"slices"
	"testing"
)

func TestSetExcludedAndCountKey(t *testing.T) {
	t.Cleanup(func() { SetExcluded(nil) })

	invalid, changed := SetExcluded([]string{
		" 8.8.8.8 ", "1.1.1.0/24", "::ffff:9.9.9.9", "2001:4860::1/32",
		"::ffff:4.4.4.0/120", "5.5.5.5/32", "bad", "1.2.3.4/33", "fe80::1%eth0", "",
	})
	if !changed {
		t.Fatal("首次设置名单应返回 changed")
	}
	if want := []string{"bad", "1.2.3.4/33", "fe80::1%eth0"}; !slices.Equal(invalid, want) {
		t.Fatalf("无效项 = %v, want %v", invalid, want)
	}

	for _, tc := range []struct {
		ip   string
		want string
	}{
		{"8.8.8.8", ""},
		{"::ffff:8.8.8.8", ""},
		{"1.1.1.200", ""},
		{"1.1.2.1", "1.1.2.1"},
		{"9.9.9.9", ""},
		{"4.4.4.9", ""},
		{"5.5.5.5", ""},
		{"2001:4860:4860::8888", ""},
		{"2606:4700::1111", "2606:4700::"},
		{"::ffff:1.0.0.1", "1.0.0.1"},
		{"10.0.0.2", ""},
		{"invalid", ""},
	} {
		if got := CountKey(tc.ip); got != tc.want {
			t.Errorf("CountKey(%q) = %q, want %q", tc.ip, got, tc.want)
		}
	}

	if _, changed := SetExcluded([]string{"5.5.5.5", "1.1.1.9/24", "9.9.9.9", "4.4.4.0/24", "2001:4860::/32", "8.8.8.8"}); changed {
		t.Fatal("等价名单不应视为变化")
	}
	if _, changed := SetExcluded(nil); !changed {
		t.Fatal("清空名单应视为变化")
	}
	if !Counts("8.8.8.8") {
		t.Fatal("清空名单后公网地址应恢复计数")
	}
	if _, changed := SetExcluded([]string{}); changed {
		t.Fatal("名单已为空时再次清空不应视为变化")
	}
}

// IPv6 按 /64 网段计为一台设备；名单按原始地址判断，精确到单个 IPv6 地址的条目仍然有效。
func TestCountKeyGroupsIPv6ByPrefix(t *testing.T) {
	t.Cleanup(func() { SetExcluded(nil) })
	SetExcluded([]string{"2400:cb00:1:2::53"})

	same := []string{"2400:cb00:1:2::1", "2400:cb00:1:2:aaaa:bbbb:cccc:dddd", "2400:cb00:0001:0002::ffff"}
	for _, ip := range same {
		if got := CountKey(ip); got != "2400:cb00:1:2::" {
			t.Errorf("CountKey(%q) = %q, want 2400:cb00:1:2::", ip, got)
		}
	}
	if got := CountKey("2400:cb00:1:3::1"); got != "2400:cb00:1:3::" {
		t.Errorf("相邻网段应单独计数，得到 %q", got)
	}
	if got := CountKey("2400:cb00:1:2::53"); got != "" {
		t.Errorf("名单内的单个 IPv6 地址不应计数，得到 %q", got)
	}
	if got := Normalize("2400:cb00:1:2::"); got != "2400:cb00:1:2::" {
		t.Errorf("面板返回的网段地址应保持不变，得到 %q", got)
	}
	if got := CountKey("8.8.8.8"); got != "8.8.8.8" {
		t.Errorf("IPv4 仍按单个地址计数，得到 %q", got)
	}
}
