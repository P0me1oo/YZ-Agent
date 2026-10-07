package sourcepolicy

import (
	"net/netip"
	"testing"
)

const testData = "198.51.100.0/24\n2001:db8::/32\n"

func TestPolicyBlocksSourcesExceptExactAddresses(t *testing.T) {
	snapshot, err := Parse([]byte(testData))
	if err != nil {
		t.Fatal(err)
	}
	p := &Policy{BlockCN: true, AllowIPs: []string{"::ffff:198.51.100.7", "2001:db8::7"}}
	blocked, err := p.Blocked(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	contains := func(ip string) bool {
		for _, entry := range blocked {
			if netip.MustParsePrefix(entry).Contains(netip.MustParseAddr(ip)) {
				return true
			}
		}
		return false
	}
	for _, ip := range []string{"198.51.100.6", "198.51.100.8", "2001:db8::6", "2001:db8::8"} {
		if !contains(ip) {
			t.Fatalf("应拦截 %s", ip)
		}
	}
	for _, ip := range []string{"198.51.100.7", "2001:db8::7", "203.0.113.8", "2001:db9::8"} {
		if contains(ip) {
			t.Fatalf("不应拦截 %s", ip)
		}
	}
	if _, err := p.Blocked(Snapshot{}); err == nil {
		t.Fatal("缺少数据库不能生成空规则")
	}
	p.BlockCN = false
	if got, err := p.Blocked(Snapshot{}); err != nil || len(got) != 0 {
		t.Fatal("关闭时不应要求网段库")
	}
}

func TestPolicyRejectsInvalidExceptions(t *testing.T) {
	for _, ip := range []string{"example.invalid", "198.51.100.0/24", "", "::%lo", "::ffff:0.0.0.0", "224.0.0.1", "ff02::1"} {
		if err := (&Policy{AllowIPs: []string{ip}}).Validate(); err == nil {
			t.Fatalf("未拒绝无效例外 %q", ip)
		}
	}
	if err := (&Policy{AllowIPs: make([]string, 129)}).Validate(); err == nil {
		t.Fatal("未限制例外数量")
	}
}

func TestDatabaseValidationAndStableRevision(t *testing.T) {
	base, err := Parse([]byte(testData))
	if err != nil {
		t.Fatal(err)
	}
	other, err := Parse([]byte("2001:db8::/32\n198.51.100.0/24\n198.51.100.0/24\n"))
	if err != nil || other.Revision != base.Revision {
		t.Fatal("顺序和重复项不应改变数据库版本")
	}
	for _, data := range []string{"", "<html>error</html>", "198.51.100.0/24", "2001:db8::/32", testData + "0.0.0.0/0", testData + "::/0", testData + "not-a-prefix"} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatal("接受了无效网段库")
		}
	}
}
