package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
)

func privacyItems(root *RootConfig) []*Config {
	if len(root.Instances) == 0 {
		return []*Config{&root.Config}
	}
	items := make([]*Config, 0, len(root.Instances))
	for i := range root.Instances {
		items = append(items, &root.Instances[i])
	}
	return items
}

func bootstrapFor(c *Config, path string) privacy.Bootstrap {
	b := privacy.Bootstrap{URL: c.Panel.URL, NodeID: c.Panel.NodeID, TokenEnv: c.Panel.TokenEnv,
		CredentialsFile: filepath.Join(filepath.Dir(path), "credentials.env")}
	if !c.IsMachineMode() && c.Panel.NodeID == 0 && len(c.Nodes) > 0 {
		b.NodeID = c.Nodes[0].NodeID
	}
	if c.PrivacyNodeID > 0 {
		b.NodeID = c.PrivacyNodeID
	}
	token := c.Panel.Token
	if c.IsMachineMode() {
		b.MachineID, b.NodeID, b.TokenEnv = c.Machine.MachineID, 0, c.Machine.TokenEnv
		token = c.Machine.Token
	} else if c.Panel.MachineID > 0 {
		b.MachineID = c.Panel.MachineID
	}
	// 仅供内存中的凭据更新使用；公开配置仍只保存环境变量引用。
	b.Token = token
	return b
}

// 兼容恢复用的整文件密文；日常配置按实例保存。
func ReadConfigData(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil || !privacy.IsEncrypted(data) {
		return data, err
	}
	if plain, err := privacy.Open(data); err == nil {
		return plain, nil
	}
	header, err := privacy.BootstrapData(data)
	var b privacy.Bootstrap
	if err != nil || json.Unmarshal(header, &b) != nil || b.URL == "" {
		return nil, errors.New("加密配置缺少启动信息")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, enabled, err := privacy.Fetch(ctx, b, path, nil)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("面板没有返回配置解密密钥，保留原文件")
	}
	return privacy.Open(data)
}

func (rc *RootConfig) openProtected(path string, strict bool) error {
	for _, c := range privacyItems(rc) {
		if c.Protected != "" {
			bootstrap := bootstrapFor(c, path)
			plain, err := openInstance(path, []byte(c.Protected), &bootstrap)
			if err != nil && !c.IsMachineMode() {
				for _, node := range c.Nodes {
					candidate := bootstrap
					candidate.NodeID = node.NodeID
					if recovered, recoverErr := openInstance(path, []byte(c.Protected), &candidate); recoverErr == nil {
						plain, err = recovered, nil
						c.PrivacyNodeID = node.NodeID
						break
					}
				}
			}
			if err != nil {
				if strict {
					return err
				}
				c.PrivacyError = err
				continue
			}
			var inner RootConfig
			if err := yaml.Unmarshal(plain, &inner); err != nil {
				if strict {
					return errors.New("加密配置内容无效")
				}
				c.PrivacyError = errors.New("加密配置内容无效")
				continue
			}
			value := inner.Config
			if len(inner.Instances) == 1 {
				value = inner.Instances[0]
				value.inheritFrom(&inner.Config)
			}
			if len(inner.Instances) > 1 || value.Protected != "" {
				if strict {
					return errors.New("加密配置实例结构无效")
				}
				c.PrivacyError = errors.New("加密配置实例结构无效")
				continue
			}
			value.PrivacyNodeID = c.PrivacyNodeID
			value.Panel.URL = c.Panel.URL
			value.Panel.TokenEnv = c.Panel.TokenEnv
			value.Panel.Token = c.Panel.Token
			if c.Machine != nil && value.Machine != nil {
				if c.Machine.MachineID != value.Machine.MachineID {
					if strict {
						return errors.New("加密配置机器身份不匹配")
					}
					c.PrivacyError = errors.New("加密配置机器身份不匹配")
					continue
				}
				value.Machine.TokenEnv = c.Machine.TokenEnv
				value.Machine.Token = c.Machine.Token
			}
			value.Protected, value.privateSource = c.Protected, plain
			*c = value
		}
		if c.PrivacyDefaults != nil {
			if c.PrivacyDefaults.PrivacyDefaults != nil {
				return errors.New("配置默认值不能递归嵌套")
			}
			c.inheritFrom(c.PrivacyDefaults)
		}
	}
	return nil
}

func openInstance(path string, data []byte, owner *privacy.Bootstrap) ([]byte, error) {
	if plain, err := privacy.Open(data); err == nil {
		return plain, nil
	}
	var b privacy.Bootstrap
	if owner != nil {
		b = *owner
	} else {
		header, err := privacy.BootstrapData(data)
		if err != nil || json.Unmarshal(header, &b) != nil {
			return nil, errors.New("加密配置启动信息无效")
		}
	}
	if b.URL == "" {
		return nil, errors.New("加密配置启动信息无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, enabled, err := privacy.Fetch(ctx, b, path, nil)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("面板不再提供该配置的解密密钥")
	}
	return privacy.Open(data)
}

func LoadWritableRoot(path string) (*RootConfig, error) {
	data, err := ReadConfigData(path)
	if err != nil {
		return nil, err
	}
	var root RootConfig
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errors.New("配置格式无效")
	}
	root.sourceHash, root.sourcePath = sha256.Sum256(data), path
	if raw, err := os.ReadFile(path); err == nil {
		root.wrapped = privacy.IsEncrypted(raw)
	}
	if err := root.openProtected(path, true); err != nil {
		return nil, err
	}
	return &root, nil
}

type privateInstance struct {
	state     *privacy.State
	bootstrap privacy.Bootstrap
}

func resolvePrivacy(ctx context.Context, path string, root *RootConfig) map[int]privateInstance {
	result := map[int]privateInstance{}
	for i, c := range privacyItems(root) {
		if c.IsStandalone() || c.PrivacyError != nil {
			continue
		}
		b := bootstrapFor(c, path)
		state, enabled, b, err := fetchInstancePrivacy(ctx, c, path, b)
		if err != nil && c.Protected != "" {
			if cached := privacy.CachedState(c.Protected); cached != nil {
				state, enabled, err = cached, true, nil
			}
		}
		if err != nil {
			c.PrivacyError = err
			continue
		}
		if !enabled {
			if c.Protected != "" {
				c.PrivacyError = errors.New("面板没有提供已有加密数据的密钥")
			}
			continue
		}
		result[i] = privateInstance{state, b}
		c.privateState, c.privateOwner = state, path
	}
	items := privacyItems(root)
	for i, a := range result {
		for j := range items {
			b, protected := result[j]
			if i == j || protected && privacy.StateKey(a.state) == privacy.StateKey(b.state) {
				continue
			}
			for _, left := range privateScopes(items[i]) {
				for _, right := range privateScopes(items[j]) {
					if left != "" && right != "" && (within(left, right) || within(right, left)) {
						items[i].PrivacyError = errors.New("不同机器不能共用同一个加密数据目录")
						items[j].PrivacyError = items[i].PrivacyError
					}
				}
			}
		}
	}
	for i := range result {
		if items[i].PrivacyError == nil {
			BindPrivacy(items[i])
		}
	}

	return result
}

func privateScopes(c *Config) []string {
	paths := []string{c.Kernel.ConfigDir, c.Cert.CertDir}
	if len(c.Nodes) > 0 {
		for _, node := range c.ExpandNodes() {
			paths = append(paths, node.Kernel.ConfigDir, node.Cert.CertDir)
		}
	}
	return paths
}

func fetchInstancePrivacy(ctx context.Context, c *Config, path string, b privacy.Bootstrap) (*privacy.State, bool, privacy.Bootstrap, error) {
	candidates := []privacy.Bootstrap{b}
	if !c.IsMachineMode() {
		for _, node := range c.Nodes {
			if node.NodeID != b.NodeID {
				next := b
				next.NodeID = node.NodeID
				candidates = append(candidates, next)
			}
		}
	}
	var last error
	answered := false
	for _, candidate := range candidates {
		state, enabled, err := privacy.Fetch(ctx, candidate, path, nil)
		if err != nil {
			last = err
			continue
		}
		answered = true
		if enabled {
			if len(c.Nodes) > 0 {
				c.PrivacyNodeID = candidate.NodeID
			}
			return state, true, candidate, nil
		}
	}
	if answered {
		return nil, false, b, nil
	}
	return nil, false, b, last
}

func BindPrivacy(c *Config) {
	if c.privateState == nil || c.PrivacyError != nil {
		return
	}
	if c.Kernel.ConfigDir != "" {
		if err := privacy.BindOwner(c.privateOwner, c.Kernel.ConfigDir, c.privateState, nil, true); err != nil {
			c.PrivacyError = err
		}
	}
	if c.Cert.CertDir != "" {
		if err := privacy.BindOwner(c.privateOwner, c.Cert.CertDir, c.privateState, nil, true); err != nil {
			c.PrivacyError = err
		}
	}
	if c.PrivacyError == nil {
		c.PrivacyError = migrateInstance(c, c.privateState)
	}
	if len(c.Nodes) > 0 {
		for _, node := range c.ExpandNodes() {
			BindPrivacy(node)
			if node.PrivacyError != nil {
				c.PrivacyError = node.PrivacyError
			}
		}
	}
}

func within(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func publicInstance(c *Config) map[string]any {
	panel := map[string]any{"url": c.Panel.URL}
	if c.Panel.TokenEnv != "" {
		panel["token_env"] = c.Panel.TokenEnv
	} else if !c.IsMachineMode() {
		panel["token"] = c.Panel.Token
	}
	if c.Panel.NodeID > 0 {
		panel["node_id"] = c.Panel.NodeID
	}
	if c.Panel.NodeType != "" {
		panel["node_type"] = c.Panel.NodeType
	}
	out := map[string]any{"id": c.InstanceID, "privacy_node_id": c.PrivacyNodeID, "panel": panel, "kernel": map[string]any{"type": c.Kernel.Type, "config_dir": c.Kernel.ConfigDir},
		"health_port": c.HealthPort, "firewall": c.Firewall, "time_sync": c.TimeSync, "runtime": c.Runtime, "log": c.Log}
	if c.IsMachineMode() {
		machine := map[string]any{"machine_id": c.Machine.MachineID}
		if c.Machine.TokenEnv != "" {
			machine["token_env"] = c.Machine.TokenEnv
		} else {
			machine["token"] = c.Machine.Token
		}
		out["machine"] = machine
	}
	if len(c.Nodes) > 0 {
		nodes := make([]map[string]any, 0, len(c.Nodes))
		for _, node := range c.Nodes {
			nodes = append(nodes, map[string]any{"node_id": node.NodeID, "node_type": node.NodeType})
		}
		out["nodes"] = nodes
	}
	return out
}

// 原始全局默认值及扩展字段随实例保存，不丢弃未识别的配置。
func encodePrivateRoot(data []byte, root *RootConfig, states map[int]privateInstance) ([]byte, error) {
	if len(states) == 0 {
		return data, nil
	}
	var original map[string]any
	if err := yaml.Unmarshal(data, &original); err != nil {
		return nil, err
	}
	var entries []any
	if values, ok := original["instances"].([]any); ok {
		entries = values
		delete(original, "instances")
	} else {
		entries = []any{original}
		original = map[string]any{}
	}
	items := privacyItems(root)
	if len(items) != len(entries) {
		return nil, errors.New("配置实例数量变化，请重新加载")
	}
	for i, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("配置实例格式无效")
		}
		state, enabled := states[i]
		if !enabled {
			if len(original) > 0 {
				entry["privacy_defaults"] = original
			}
			continue
		}
		inner := map[string]any{}
		for key, value := range original {
			inner[key] = value
		}
		inner["instances"] = []any{entry}
		if len(items[i].privateSource) > 0 && items[i].Protected == "" {
			var saved map[string]any
			if err := yaml.Unmarshal(items[i].privateSource, &saved); err != nil {
				return nil, errors.New("原加密配置内容无效")
			}
			previous := saved
			if values, ok := saved["instances"].([]any); ok && len(values) == 1 {
				previous, _ = values[0].(map[string]any)
			}
			delete(previous, "protected")
			delete(previous, "privacy_defaults")
			saved["instances"] = []any{mergeFields(previous, entry)}
			inner = mergeFields(saved, inner)
			inner["instances"] = saved["instances"]
		}
		plain, err := yaml.Marshal(inner)
		if err != nil {
			return nil, err
		}
		existing, _ := entry["protected"].(string)
		if existing != "" {
			owner := state.bootstrap
			plain, err = openInstance(pathForBootstrap(state.bootstrap), []byte(existing), &owner)
			if err != nil {
				return nil, err
			}
		}
		sealed, err := state.state.Seal(plain, nil)
		if err != nil {
			return nil, err
		}
		if existing != "" && privacy.CurrentKey(existing) == privacy.StateKey(state.state) {
			sealed = []byte(existing)
		}
		outer := publicInstance(items[i])
		if id, ok := entry["id"].(string); ok && id != "" {
			outer["id"] = id
		}
		outer["protected"] = string(sealed)
		entries[i] = outer
	}
	return yaml.Marshal(map[string]any{"instances": entries})
}

func pathForBootstrap(b privacy.Bootstrap) string {
	return filepath.Join(filepath.Dir(b.CredentialsFile), "config.yml")
}

func PreparePrivacy(ctx context.Context, path string, root *RootConfig) error {
	unlock, err := privacy.ConfigLock(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	before, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := ReadConfigData(path)
	if err != nil {
		return err
	}
	if root.sourcePath == path && root.sourceHash != sha256.Sum256(data) {
		return errors.New("配置已变化，请重新加载")
	}
	credentialPath := path
	if root.sourcePath != "" {
		credentialPath = root.sourcePath
	}
	states := resolvePrivacy(ctx, credentialPath, root)
	if len(states) == 0 {
		root.PrivacyReady = true
		return nil
	}
	encoded, err := encodePrivateRoot(data, root, states)
	if err != nil {
		return err
	}
	items := privacyItems(root)
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, before) {
		return errors.New("配置已变化，稍后重新保护")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !bytes.Equal(encoded, before) {
		if err := privacy.WriteRaw(path, encoded, 0o600); err != nil {
			return err
		}
	}
	root.sourceHash, root.sourcePath, root.PrivacyReady = sha256.Sum256(encoded), path, true
	if err := MinimizeInstallMeta(filepath.Join(filepath.Dir(path), "install-meta.json")); err != nil {
		return err
	}
	for index, state := range states {
		if err := migrateBackups(filepath.Join(filepath.Dir(path), "backups"), state); err != nil {
			items[index].PrivacyError = errors.New("历史备份保护未完成，保留原文件，稍后重试")
		}
	}
	return nil
}

func migrateInstance(c *Config, state *privacy.State) error {
	for _, dir := range []string{c.Kernel.ConfigDir, c.Cert.CertDir} {
		if dir == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			name := entry.Name()
			if path == c.Kernel.CustomConfig && within(c.Kernel.ConfigDir, path) || strings.HasPrefix(name, "pending-") || strings.HasPrefix(name, "machine-traffic-") ||
				strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".crt") ||
				within(c.Cert.CertDir, path) && strings.HasSuffix(name, ".json") {
				return privacy.MigrateFile(path, state)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func migrateBackups(dir string, state privateInstance) error {
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != "config.yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || privacy.IsEncrypted(data) {
			return err
		}
		var root RootConfig
		if yaml.Unmarshal(data, &root) != nil {
			return nil
		}
		matched := map[int]privateInstance{}
		for i, c := range privacyItems(&root) {
			if c.PrivacyDefaults != nil {
				c.inheritFrom(c.PrivacyDefaults)
			}
			c.inheritFrom(&root.Config)
			b := bootstrapFor(c, path)
			if b.URL == state.bootstrap.URL && b.MachineID == state.bootstrap.MachineID && b.NodeID == state.bootstrap.NodeID && b.Credential(path) != "" {
				matched[i] = privateInstance{state.state, b}
			}
		}
		// 不改写无法确认所属身份、无法自动恢复的历史备份。
		if len(matched) == 0 {
			return nil
		}
		encoded, err := encodePrivateRoot(data, &root, matched)
		if err != nil {
			return err
		}
		if bytes.Equal(encoded, data) {
			return nil
		}
		return privacy.WriteRaw(path, encoded, 0o600)
	})
}

// 安装记录保留版本和实例摘要，不重复保存连接入口。
func MinimizeInstallMeta(path string) error {
	data, err := privacy.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return errors.New("安装记录无效，保留原文件")
	}
	delete(metadata, "panel_url")
	if items, ok := metadata["instances"].([]any); ok {
		for _, item := range items {
			if row, ok := item.(map[string]any); ok {
				delete(row, "panel_url")
			}
		}
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if bytes.Equal(data, encoded) {
		return nil
	}
	return privacy.WriteRaw(path, encoded, 0o600)
}

func WriteRootData(path string, data []byte, root *RootConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := privacy.ConfigLock(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	if root.ExportLegacy {
		return privacy.WriteRaw(path, data, 0o600)
	}
	if root.sourcePath == path {
		current, err := ReadConfigData(path)
		if err != nil {
			return err
		}
		if root.sourceHash != sha256.Sum256(current) {
			return errors.New("配置已变化，拒绝覆盖其他修改")
		}
	}
	protected := root.wrapped
	for _, c := range privacyItems(root) {
		protected = protected || c.Protected != ""
	}
	if !protected {
		return privacy.WriteFile(path, data, 0o600)
	}
	credentialPath := path
	if root.sourcePath != "" {
		credentialPath = root.sourcePath
	}
	states := resolvePrivacy(ctx, credentialPath, root)
	for _, c := range privacyItems(root) {
		if c.PrivacyError != nil {
			return c.PrivacyError
		}
	}
	copyRoot := *root
	copyRoot.Instances = append([]Config(nil), root.Instances...)
	copyRoot.Config.Protected = ""
	for i := range copyRoot.Instances {
		copyRoot.Instances[i].Protected = ""
	}
	encoded, err := encodePrivateRoot(data, &copyRoot, states)
	if err != nil {
		return err
	}
	if err := privacy.WriteRaw(path, encoded, 0o600); err != nil {
		return err
	}
	root.sourceHash, root.sourcePath = sha256.Sum256(encoded), path
	return nil
}

// 策略变化或面板恢复后自动重试；已运行的实例不因一次领取失败被停止。
func WatchPrivacy(ctx context.Context, path string, initial *RootConfig, ready func(*RootConfig)) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	failed := map[int]bool{}
	for i, c := range privacyItems(initial) {
		failed[i] = c.PrivacyError != nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		root, err := LoadRoot(path)
		if err != nil || ctx.Err() != nil {
			continue
		}
		if err := PreparePrivacy(ctx, path, root); err != nil {
			continue
		}
		for i, c := range privacyItems(root) {
			if failed[i] && c.PrivacyError == nil {
				ready(root)
				return
			}
		}
	}
}
