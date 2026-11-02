package config

import (
	"context"
	"errors"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
)

// 仅供明确选择旧版或失败回滚时使用。调用前必须停止服务。
func ExportPrivateState(ctx context.Context, path, backup string) error {
	root, err := LoadWritableRoot(path)
	if err != nil {
		return err
	}
	for _, c := range privacyItems(root) {
		c.inheritFrom(&root.Config)
	}
	if err := root.assignInstanceIDs(); err != nil {
		return err
	}
	root.setDefaultsFrom(configBaseDir(path))
	var encrypted []string
	items := append([]*Config(nil), privacyItems(root)...)
	for _, c := range privacyItems(root) {
		if len(c.Nodes) > 0 {
			items = append(items, c.ExpandNodes()...)
		}
	}
	for _, c := range items {
		for _, dir := range []string{c.Kernel.ConfigDir, c.Cert.CertDir} {
			if dir == "" {
				continue
			}
			if err := filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				base := entry.Name()
				if !strings.HasPrefix(base, "pending-") && !strings.HasPrefix(base, "machine-traffic-") && !strings.HasSuffix(base, ".pem") && !strings.HasSuffix(base, ".key") && !strings.HasSuffix(base, ".crt") && !(within(c.Cert.CertDir, name) && strings.HasSuffix(base, ".json")) && name != c.Kernel.CustomConfig {
					return nil
				}
				raw, err := os.ReadFile(name)
				if err != nil {
					return err
				}
				if privacy.IsEncrypted(raw) {
					encrypted = append(encrypted, name)
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	if len(encrypted) > 0 {
		_ = resolvePrivacy(ctx, path, root)
		for _, c := range privacyItems(root) {
			if c.PrivacyError != nil {
				return c.PrivacyError
			}
		}
		for _, name := range encrypted {
			raw, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			plain, err := privacy.Open(raw)
			if err != nil {
				return err
			}
			if err := privacy.WriteRaw(name, plain, 0o600); err != nil {
				return err
			}
		}
	}
	if backup != "" {
		if filepath.Base(backup) == "." || filepath.Clean(backup) == string(filepath.Separator) {
			return errors.New("备份目录无效")
		}
		for _, file := range []string{"config.yml", "install-meta.json"} {
			name := filepath.Join(backup, file)
			raw, err := os.ReadFile(name)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !hasCipherConfig(raw) && !privacy.IsEncrypted(raw) {
				continue
			}
			plain, err := ReadConfigData(name)
			if file == "config.yml" {
				plain, err = ExportLegacyConfig(name)
			}
			if err != nil {
				return err
			}
			if err := privacy.WriteRaw(name, plain, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasCipherConfig(data []byte) bool {
	if privacy.IsEncrypted(data) {
		return true
	}
	var root struct {
		Protected string `yaml:"protected"`
		Instances []struct {
			Protected string `yaml:"protected"`
		} `yaml:"instances"`
	}
	if yaml.Unmarshal(data, &root) != nil {
		return false
	}
	if strings.HasPrefix(root.Protected, privacy.Magic) {
		return true
	}
	for _, c := range root.Instances {
		if strings.HasPrefix(c.Protected, privacy.Magic) {
			return true
		}
	}
	return false
}

// 还原旧版配置，并保留未识别的扩展字段。
func ExportLegacyConfig(path string) ([]byte, error) {
	data, err := ReadConfigData(path)
	if err != nil {
		return nil, err
	}
	root, err := LoadWritableRoot(path)
	if err != nil {
		return nil, err
	}
	var original map[string]any
	if err := yaml.Unmarshal(data, &original); err != nil {
		return nil, err
	}
	defaultsData, _ := yaml.Marshal(Config{})
	var known map[string]any
	_ = yaml.Unmarshal(defaultsData, &known)
	result := map[string]any{}
	copyExtras := func(values map[string]any) {
		for name, value := range values {
			if _, ok := known[name]; !ok && name != "instances" {
				result[name] = value
			}
		}
	}
	copyExtras(original)
	entries, _ := original["instances"].([]any)
	if entries == nil {
		entries = []any{original}
	}
	output := make([]any, 0, len(entries))
	for i, c := range privacyItems(root) {
		if i >= len(entries) {
			return nil, errors.New("备份实例数量无效")
		}
		entry, _ := entries[i].(map[string]any)
		if c.Protected != "" {
			b := bootstrapFor(c, path)
			plain, err := openInstance(path, []byte(c.Protected), &b)
			if err != nil {
				return nil, err
			}
			var inner map[string]any
			if err := yaml.Unmarshal(plain, &inner); err != nil {
				return nil, err
			}
			copyExtras(inner)
			if values, ok := inner["instances"].([]any); ok && len(values) == 1 {
				entry, _ = values[0].(map[string]any)
			} else {
				entry = inner
			}
		}
		if shared, ok := entry["privacy_defaults"].(map[string]any); ok {
			copyExtras(shared)
		}
		value := *c
		value.inheritFrom(&root.Config)
		value.Protected = ""
		value.PrivacyDefaults = nil
		body, err := yaml.Marshal(value)
		if err != nil {
			return nil, err
		}
		var fields map[string]any
		if err := yaml.Unmarshal(body, &fields); err != nil {
			return nil, err
		}
		delete(entry, "protected")
		delete(entry, "privacy_defaults")
		output = append(output, mergeFields(entry, fields))
	}
	result["instances"] = output
	return yaml.Marshal(result)
}

func mergeFields(original, known map[string]any) map[string]any {
	result := map[string]any{}
	for k, v := range original {
		result[k] = v
	}
	for k, v := range known {
		left, ok := result[k].(map[string]any)
		right, both := v.(map[string]any)
		if ok && both {
			result[k] = mergeFields(left, right)
		} else {
			result[k] = v
		}
	}
	return result
}
