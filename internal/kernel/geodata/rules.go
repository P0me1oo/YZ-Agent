package geodata

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	xgeo "github.com/xtls/xray-core/common/geodata"
	"go4.org/netipx"
	"google.golang.org/protobuf/proto"
)

// LoadRules 将 Xray 的分类数据转成通用匹配字段，供 sing-box 内联规则集使用。
// 两种核心读取相同的 dat 文件，不另外下载一份可能覆盖范围不同的分类库。
func LoadRules(dir string, references []string) (map[string]map[string]any, error) {
	needIP, needSite := false, false
	for _, ref := range references {
		needIP = needIP || strings.HasPrefix(ref, "geoip:")
		needSite = needSite || strings.HasPrefix(ref, "geosite:")
	}
	if err := Ensure(dir, needIP, needSite, "xray"); err != nil {
		return nil, fmt.Errorf("prepare shared route data: %w", err)
	}
	sites := make(map[string]*xgeo.GeoSite)
	ips := make(map[string]*xgeo.GeoIP)
	if needSite {
		var list xgeo.GeoSiteList
		if err := readData(filepath.Join(dir, "geosite.dat"), &list); err != nil {
			return nil, err
		}
		for _, entry := range list.Entry {
			if entry == nil {
				continue
			}
			code := strings.ToLower(entry.Code)
			if sites[code] == nil {
				sites[code] = entry
			}
		}
	}
	if needIP {
		var list xgeo.GeoIPList
		if err := readData(filepath.Join(dir, "geoip.dat"), &list); err != nil {
			return nil, err
		}
		for _, entry := range list.Entry {
			if entry == nil {
				continue
			}
			code := strings.ToLower(entry.Code)
			if ips[code] == nil {
				ips[code] = entry
			}
		}
	}
	result := make(map[string]map[string]any, len(references))
	for _, ref := range references {
		if result[ref] != nil {
			continue
		}
		var rule map[string]any
		var err error
		switch {
		case strings.HasPrefix(ref, "geosite:"):
			rule, err = siteRule(strings.TrimPrefix(ref, "geosite:"), sites)
		case strings.HasPrefix(ref, "geoip:"):
			rule, err = ipRule(strings.TrimPrefix(ref, "geoip:"), ips)
		default:
			err = fmt.Errorf("unsupported classification %q", ref)
		}
		if err != nil {
			return nil, fmt.Errorf("route classification %q: %w", ref, err)
		}
		result[ref] = rule
	}
	return result, nil
}

func readData(path string, message proto.Message) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read route data %q: %w", path, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("route data %q is empty", path)
	}
	if err := proto.Unmarshal(data, message); err != nil {
		return fmt.Errorf("decode route data %q: %w", path, err)
	}
	return nil
}

func siteRule(value string, sites map[string]*xgeo.GeoSite) (map[string]any, error) {
	code, attrs, _ := strings.Cut(strings.ToLower(value), "@")
	site := sites[code]
	if site == nil {
		return nil, fmt.Errorf("domain category %q is missing from geosite.dat", code)
	}
	matcher := xgeo.NewAllAttrsMatcher(attrs)
	fields := make(map[string][]string)
	for _, domain := range site.Domain {
		if domain == nil || domain.Value == "" || (matcher != nil && !matcher.Match(domain)) {
			continue
		}
		var field string
		switch domain.Type {
		case xgeo.Domain_Substr:
			field = "domain_keyword"
		case xgeo.Domain_Regex:
			field = "domain_regex"
		case xgeo.Domain_Domain:
			field = "domain_suffix"
		case xgeo.Domain_Full:
			field = "domain"
		default:
			return nil, fmt.Errorf("unsupported domain match type %d", domain.Type)
		}
		value := domain.Value
		if domain.Type != xgeo.Domain_Regex {
			value = strings.ToLower(value)
		}
		fields[field] = append(fields[field], value)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("domain category %q has no matching entries", value)
	}
	rule := make(map[string]any, len(fields))
	for field, values := range fields {
		rule[field] = values
	}
	return rule, nil
}

func ipRule(value string, ips map[string]*xgeo.GeoIP) (map[string]any, error) {
	value = strings.ToLower(value)
	reverse := false
	for strings.HasPrefix(value, "!") {
		reverse = !reverse
		value = strings.TrimPrefix(value, "!")
	}
	code := value
	entry := ips[code]
	if entry == nil {
		return nil, fmt.Errorf("ip category %q is missing from geoip.dat", code)
	}
	cidrs := make([]string, 0, len(entry.Cidr))
	var prefixes []netip.Prefix
	var has4, has6 bool
	for _, cidr := range entry.Cidr {
		if cidr == nil {
			return nil, fmt.Errorf("ip category %q contains an empty cidr", code)
		}
		address, ok := netip.AddrFromSlice(cidr.Ip)
		if !ok || cidr.Prefix > uint32(address.BitLen()) {
			return nil, fmt.Errorf("ip category %q contains an invalid cidr", code)
		}
		prefix := netip.PrefixFrom(address, int(cidr.Prefix)).Masked()
		cidrs = append(cidrs, prefix.String())
		prefixes = append(prefixes, prefix)
		has4 = has4 || address.Is4()
		has6 = has6 || address.Is6()
	}
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("ip category %q has no entries", code)
	}
	if !reverse {
		return map[string]any{"ip_cidr": cidrs}, nil
	}
	// Xray 只在分类已有的地址族内反选；补集也保持多地址解析时的“任一命中”。
	var builder netipx.IPSetBuilder
	if has4 {
		builder.AddPrefix(netip.MustParsePrefix("0.0.0.0/0"))
	}
	if has6 {
		builder.AddPrefix(netip.MustParsePrefix("::/0"))
	}
	for _, prefix := range prefixes {
		builder.RemovePrefix(prefix)
	}
	set, err := builder.IPSet()
	if err != nil {
		return nil, fmt.Errorf("invert ip category %q: %w", code, err)
	}
	cidrs = nil
	for _, prefix := range set.Prefixes() {
		cidrs = append(cidrs, prefix.String())
	}
	if len(cidrs) == 0 {
		// 空补集必须始终不命中，不能生成空规则而匹配全部连接。
		return map[string]any{"type": "logical", "mode": "and", "rules": []map[string]any{
			{"ip_cidr": []string{"0.0.0.0/0", "::/0"}},
			{"ip_cidr": []string{"0.0.0.0/0", "::/0"}, "invert": true},
		}}, nil
	}
	return map[string]any{"ip_cidr": cidrs}, nil
}
