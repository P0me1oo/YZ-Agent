// Package xhttp 统一面板参数与当前 Xray XHTTP 配置的语义。
package xhttp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
)

func Is(network string) bool {
	return strings.EqualFold(strings.TrimSpace(network), "xhttp") || strings.EqualFold(strings.TrimSpace(network), "splithttp")
}

// Normalize 不修改输入；extra 按核心规则替换高级字段，空对象恢复为映射。
func Normalize(input map[string]any) (map[string]any, error) {
	return normalize(input, false)
}

func normalize(input map[string]any, downloading bool) (map[string]any, error) {
	out := make(map[string]any, len(input))
	source := input
	if value, ok := input["extra"]; ok && value != nil {
		extra, err := object(value)
		if err != nil {
			return nil, fmt.Errorf("extra: %w", err)
		}
		source = extra
	}
	for key, value := range source {
		if key != "extra" {
			out[key] = value
		}
	}
	if sourceIsExtra(input) {
		for _, key := range []string{"path", "host", "mode"} {
			delete(out, key)
			if value, ok := input[key]; ok {
				out[key] = value
			}
		}
	}
	for old, current := range map[string]string{"sessionPlacement": "sessionIDPlacement", "sessionKey": "sessionIDKey"} {
		if _, ok := out[current]; !ok {
			if value, exists := out[old]; exists {
				out[current] = value
			}
		}
		delete(out, old)
	}
	for _, key := range []string{"headers", "xmux", "downloadSettings", "h2Params"} {
		if value, ok := out[key]; ok && value != nil {
			obj, err := object(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			copyObj := make(map[string]any, len(obj))
			for k, v := range obj {
				copyObj[k] = v
			}
			out[key] = copyObj
		}
	}
	if xmux, ok := out["xmux"].(map[string]any); ok {
		if legacy, exists := xmux["cMaxLifetimeMs"]; exists {
			if _, set := xmux["hMaxReusableSecs"]; !set {
				b, err := json.Marshal(legacy)
				if err != nil {
					return nil, err
				}
				var r conf.Int32Range
				if err = json.Unmarshal(b, &r); err != nil {
					return nil, fmt.Errorf("cMaxLifetimeMs: %w", err)
				}
				lo, hi := int64(r.From), int64(r.To)
				if lo < 0 || hi < 0 {
					return nil, fmt.Errorf("cMaxLifetimeMs must not be negative")
				}
				lo = (lo + 999) / 1000
				hi = (hi + 999) / 1000
				if lo == hi {
					xmux["hMaxReusableSecs"] = lo
				} else {
					xmux["hMaxReusableSecs"] = fmt.Sprintf("%d-%d", lo, hi)
				}
			}
			delete(xmux, "cMaxLifetimeMs")
		}
	}
	if download, ok := out["downloadSettings"].(map[string]any); ok {
		if downloading {
			return nil, fmt.Errorf("downloadSettings cannot nest another downloadSettings")
		}
		if _, exists := download["network"]; !exists {
			download["network"] = "xhttp"
		}
		if value, ok := download["port"].(string); ok {
			if port, err := strconv.ParseUint(value, 10, 16); err == nil {
				download["port"] = port
			}
		}
		if _, exists := download["xhttpSettings"]; !exists {
			if legacy, set := download["splithttpSettings"]; set {
				download["xhttpSettings"] = legacy
			}
		}
		delete(download, "splithttpSettings")
		for _, key := range []string{"tlsSettings", "realitySettings", "sockopt", "xhttpSettings"} {
			if value, set := download[key]; set && value != nil {
				obj, err := object(value)
				if err != nil {
					return nil, fmt.Errorf("downloadSettings.%s: %w", key, err)
				}
				if key == "xhttpSettings" {
					obj, err = normalize(obj, true)
					if err != nil {
						return nil, err
					}
				}
				download[key] = obj
			}
		}

		if download["security"] == "reality" {
			value, _ := download["realitySettings"].(map[string]any)
			settings := make(map[string]any, len(value)+1)
			for key, item := range value {
				settings[key] = item
			}
			if value, ok := settings["fingerprint"]; !ok || value == "" {
				settings["fingerprint"] = "chrome"
			}
			download["realitySettings"] = settings
		}
	}
	return out, nil
}

func sourceIsExtra(input map[string]any) bool { value, ok := input["extra"]; return ok && value != nil }

func object(value any) (map[string]any, error) {
	switch v := value.(type) {
	case map[string]any:
		return v, nil
	case []any:
		if len(v) == 0 {
			return map[string]any{}, nil
		}
	}
	return nil, fmt.Errorf("must be an object")
}

func Validate(settings map[string]any) error {
	normalized, err := Normalize(settings)
	if err != nil {
		return err
	}
	if d, ok := normalized["downloadSettings"].(map[string]any); ok {
		address, ok := d["address"].(string)
		if !ok || strings.TrimSpace(address) == "" {
			return fmt.Errorf("downloadSettings requires a non-empty address")
		}
		network, _ := d["network"].(string)
		if !Is(network) {
			return fmt.Errorf("downloadSettings requires xhttp network")
		}
		b, _ := json.Marshal(d["port"])
		var port uint16
		if err := json.Unmarshal(b, &port); err != nil || port == 0 {
			return fmt.Errorf("downloadSettings requires a port between 1 and 65535")
		}
		if transport, ok := d["xhttpSettings"].(map[string]any); ok {
			if err = Validate(transport); err != nil {
				return err
			}
		}
	}
	// 核心会按长度范围遍历计算会话空间；先限制长度，避免巨大范围阻塞校验。
	if table, ok := normalized["sessionIDTable"].(string); ok && table != "" {
		b, _ := json.Marshal(normalized["sessionIDLength"])
		var length conf.Int32Range
		if err = json.Unmarshal(b, &length); err != nil || length.From < 1 || length.To > 128 {
			return fmt.Errorf("sessionIDLength must be between 1 and 128")
		}
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	var c conf.SplitHTTPConfig
	if err = json.Unmarshal(b, &c); err != nil {
		return err
	}
	_, err = c.Build()
	return err
}

func ALPN(settings map[string]any) []string {
	switch value := settings["alpn"].(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func IsH3(network string, tls int, settings map[string]any) bool {
	alpn := ALPN(settings)
	return Is(network) && tls == 1 && len(alpn) == 1 && alpn[0] == "h3"
}
