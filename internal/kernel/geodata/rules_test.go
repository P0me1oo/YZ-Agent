package geodata

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	xgeo "github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

func writeData(t *testing.T, dir, name string, message proto.Message) {
	t.Helper()
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeData(t, dir, "geosite.dat", &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST", Domain: []*xgeo.Domain{
		{Type: xgeo.Domain_Domain, Value: "example.test"},
		{Type: xgeo.Domain_Full, Value: "only.test", Attribute: []*xgeo.Domain_Attribute{{Key: "cn"}, {Key: "ads"}}},
		{Type: xgeo.Domain_Substr, Value: "keyword"},
		{Type: xgeo.Domain_Regex, Value: `^r[0-9]+\.test$`},
	}}}})
	writeData(t, dir, "geoip.dat", &xgeo.GeoIPList{Entry: []*xgeo.GeoIP{{Code: "TEST", ReverseMatch: true, Cidr: []*xgeo.CIDR{
		{Ip: netip.MustParseAddr("192.0.2.123").AsSlice(), Prefix: 24},
		{Ip: netip.MustParseAddr("2001:db8::123").AsSlice(), Prefix: 32},
	}}}})
	references := []string{"geosite:test", "geosite:TEST@cn@ads", "geoip:test", "geoip:!!test", "geosite:test"}
	want := map[string]map[string]any{
		"geosite:test": {"domain_suffix": []string{"example.test"}, "domain": []string{"only.test"},
			"domain_keyword": []string{"keyword"}, "domain_regex": []string{`^r[0-9]+\.test$`}},
		"geosite:TEST@cn@ads": {"domain": []string{"only.test"}},
		"geoip:test":          {"ip_cidr": []string{"192.0.2.0/24", "2001:db8::/32"}},
		"geoip:!!test":        {"ip_cidr": []string{"192.0.2.0/24", "2001:db8::/32"}},
	}
	// 多实例同时读相同文件，不下载 db，也不相互修改匹配结果。
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := LoadRules(dir, references)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("LoadRules = %#v, %v; want %#v", got, err, want)
			}
		})
	}
	wg.Wait()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("shared files = %v, %v", files, err)
	}
}

func TestLoadRulesRejectsInvalidData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ref  string
		data proto.Message
		file string
		want string
	}{
		{"missing category", "geosite:missing", &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST"}}}, "geosite.dat", "missing"},
		{"empty category", "geosite:test", &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST"}}}, "geosite.dat", "no matching entries"},
		{"missing attribute", "geosite:test@cn", &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST", Domain: []*xgeo.Domain{{Value: "example.test"}}}}}, "geosite.dat", "no matching entries"},
		{"unknown type", "geosite:test", &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST", Domain: []*xgeo.Domain{{Type: 99, Value: "example.test"}}}}}, "geosite.dat", "unsupported domain"},
		{"empty IP category", "geoip:test", &xgeo.GeoIPList{Entry: []*xgeo.GeoIP{{Code: "TEST"}}}, "geoip.dat", "no entries"},
		{"invalid IP", "geoip:test", &xgeo.GeoIPList{Entry: []*xgeo.GeoIP{{Code: "TEST", Cidr: []*xgeo.CIDR{{Ip: []byte{1}, Prefix: 8}}}}}, "geoip.dat", "invalid cidr"},
		{"invalid prefix", "geoip:test", &xgeo.GeoIPList{Entry: []*xgeo.GeoIP{{Code: "TEST", Cidr: []*xgeo.CIDR{{Ip: []byte{1, 2, 3, 4}, Prefix: 33}}}}}, "geoip.dat", "invalid cidr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeData(t, dir, tc.file, tc.data)
			got, err := LoadRules(dir, []string{tc.ref})
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, %v; want error containing %q", got, err, tc.want)
			}
		})
	}
	for _, data := range [][]byte{nil, {0xff}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "geosite.dat"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRules(dir, []string{"geosite:test"}); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}

func TestIPRuleEmptyComplementNeverMatches(t *testing.T) {
	t.Parallel()
	got, err := ipRule("!all", map[string]*xgeo.GeoIP{"all": {Cidr: []*xgeo.CIDR{
		{Ip: []byte{0, 0, 0, 0}, Prefix: 0}, {Ip: make([]byte, 16), Prefix: 0},
	}}})
	want := map[string]any{"type": "logical", "mode": "and", "rules": []map[string]any{
		{"ip_cidr": []string{"0.0.0.0/0", "::/0"}},
		{"ip_cidr": []string{"0.0.0.0/0", "::/0"}, "invert": true},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("empty complement = %#v, %v", got, err)
	}
}
