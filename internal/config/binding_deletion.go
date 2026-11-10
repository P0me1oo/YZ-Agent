package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/binding"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
)

type deletedBinding struct {
	id         string
	credential privacy.Bootstrap
	token      string
}

// Filter only public binding envelopes. A deleted machine may no longer be
// able to decrypt its data; unrelated ciphertext and unknown fields stay intact.
func filterDeletedBindings(ctx context.Context, path string, before []byte) ([]byte, []deletedBinding, error) {
	if privacy.IsEncrypted(before) {
		// Legacy recovery envelopes can contain multiple hidden identities. Without
		// public per-instance boundaries their bootstrap is insufficient to prune.
		return before, nil, nil
	}
	var document map[string]any
	if err := yaml.Unmarshal(before, &document); err != nil {
		return nil, nil, err
	}
	var root RootConfig
	if err := yaml.Unmarshal(before, &root); err != nil {
		return nil, nil, err
	}
	entries, multi := document["instances"].([]any)
	if !multi {
		entries = []any{document}
	}
	var removed []deletedBinding
	kept := make([]any, 0, len(entries))
	for _, entry := range entries {
		raw, err := yaml.Marshal(entry)
		if err != nil {
			return nil, nil, err
		}
		var c Config
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return nil, nil, err
		}
		c.inheritFrom(&root.Config)
		if !c.IsMachineMode() {
			kept = append(kept, entry)
			continue
		}
		b := bootstrapFor(&c, path)
		// Global endpoint/identity overrides do not authorize deleting a different
		// identity stored on disk.
		if endpoint := os.Getenv("PANEL_URL"); endpoint != "" && endpoint != b.URL {
			kept = append(kept, entry)
			continue
		}
		if machineID := os.Getenv("MACHINE_ID"); machineID != "" && machineID != strconv.Itoa(b.MachineID) {
			kept = append(kept, entry)
			continue
		}
		token := b.Credential(path)
		deleted, _ := binding.Deleted(ctx, b.URL, b.MachineID, token)
		if !deleted {
			kept = append(kept, entry)
			continue
		}
		id, _ := c.AutoInstanceID()
		removed = append(removed, deletedBinding{id: id, credential: b, token: token})
	}
	if len(removed) == 0 {
		return before, nil, nil
	}
	if !multi {
		// Remove the identity envelope, retain shared operational defaults.
		for _, key := range []string{"panel", "machine", "node", "nodes", "id", "protected", "privacy_defaults", "privacy_node_id"} {
			delete(document, key)
		}
	}
	document["instances"] = kept
	if len(kept) == 0 {
		document["binding_idle"] = true
		if multi && root.HealthPort == 0 && len(root.Instances) > 0 {
			document["health_port"] = root.Instances[0].HealthPort
		}
	} else {
		delete(document, "binding_idle")
	}
	after, err := yaml.Marshal(document)
	return after, removed, err
}

// PruneDeletedMachines verifies deletion before taking the local write lock,
// then compares config and credentials again so a concurrent rebind wins.
// It never removes data directories, certificates, pending traffic or backups.
func PruneDeletedMachines(ctx context.Context, path string) (bool, error) {
	if err := finishPendingBindingCleanup(ctx, path); err != nil {
		return false, err
	}
	before, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	credentialsPath := filepath.Join(filepath.Dir(path), "credentials.env")
	credentials, credErr := os.ReadFile(credentialsPath)
	if credErr != nil && !os.IsNotExist(credErr) {
		return false, credErr
	}
	after, removed, err := filterDeletedBindings(ctx, path, before)
	if err != nil || len(removed) == 0 {
		return false, err
	}
	unlock, err := privacy.ConfigLock(ctx, path)
	if err != nil {
		return false, err
	}
	defer unlock()
	current, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	currentCredentials, err := os.ReadFile(credentialsPath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if !bytes.Equal(before, current) || !bytes.Equal(credentials, currentCredentials) {
		return false, errors.New("绑定配置已变化，稍后重新核对")
	}
	for _, item := range removed {
		if item.credential.Credential(path) != item.token {
			return false, errors.New("绑定凭据已变化，稍后重新核对")
		}
	}
	// Config is authoritative and committed first; credentials are never removed
	// while an active binding still references them.
	queue := bindingCleanup{ConfigHash: fmt.Sprintf("%x", sha256.Sum256(after))}
	for _, item := range removed {
		queue.Bindings = append(queue.Bindings, bindingCleanupItem{ID: item.id, TokenEnv: item.credential.TokenEnv})
	}
	queued, err := json.Marshal(queue)
	if err != nil {
		return false, err
	}
	queuePath := filepath.Join(filepath.Dir(path), ".binding-cleanup.json")
	if err := privacy.WriteRaw(queuePath, queued, 0o600); err != nil {
		return false, err
	}
	if err := privacy.WriteRaw(path, after, 0o600); err != nil {
		return false, err
	}
	return true, applyBindingCleanup(path, after, queue, queuePath)
}

// A tiny local completion record contains only IDs and environment variable
// names. Resume ancillary writes after a crash without asking a deleted
// identity for authentication again. A concurrent config replacement wins.
type bindingCleanupItem struct {
	ID       string `json:"id"`
	TokenEnv string `json:"token_env"`
}
type bindingCleanup struct {
	ConfigHash string               `json:"config_hash"`
	Bindings   []bindingCleanupItem `json:"bindings"`
}

func finishPendingBindingCleanup(ctx context.Context, path string) error {
	queuePath := filepath.Join(filepath.Dir(path), ".binding-cleanup.json")
	if _, err := os.Stat(queuePath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := privacy.ConfigLock(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := os.ReadFile(queuePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var queue bindingCleanup
	if err := json.Unmarshal(data, &queue); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(current)) != queue.ConfigHash {
		// Either config commit never happened or another writer replaced it.
		return os.Remove(queuePath)
	}
	return applyBindingCleanup(path, current, queue, queuePath)
}

func applyBindingCleanup(path string, data []byte, queue bindingCleanup, queuePath string) error {
	var removed []deletedBinding
	for _, item := range queue.Bindings {
		removed = append(removed, deletedBinding{id: item.ID, credential: privacy.Bootstrap{TokenEnv: item.TokenEnv}})
	}
	credentialPath := filepath.Join(filepath.Dir(path), "credentials.env")
	credentials, err := os.ReadFile(credentialPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := pruneDeletedCredentials(credentialPath, credentials, data, removed); err != nil {
		return err
	}
	if err := pruneDeletedMetadata(filepath.Join(filepath.Dir(path), "install-meta.json"), removed); err != nil {
		return err
	}
	return os.Remove(queuePath)
}

func pruneDeletedCredentials(path string, before, configData []byte, removed []deletedBinding) error {
	keys := map[string]bool{}
	for _, item := range removed {
		if item.credential.TokenEnv != "" {
			keys[item.credential.TokenEnv] = true
		}
	}
	// token_env can be shared with another machine or node. Search nested defaults
	// as well as public protected envelopes before dropping the variable.
	var value any
	if err := yaml.Unmarshal(configData, &value); err != nil {
		return err
	}
	var keepRefs func(any)
	keepRefs = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if key, ok := v["token_env"].(string); ok {
				delete(keys, key)
			}
			for _, child := range v {
				keepRefs(child)
			}
		case []any:
			for _, child := range v {
				keepRefs(child)
			}
		}
	}
	keepRefs(value)
	var lines []string
	for _, line := range strings.Split(string(before), "\n") {
		key, _, _ := strings.Cut(strings.TrimSpace(line), "=")
		if !keys[key] {
			lines = append(lines, line)
		}
	}
	after := []byte(strings.Join(lines, "\n"))
	if bytes.Equal(before, after) {
		return nil
	}
	return privacy.WriteRaw(path, after, 0o600)
}

func pruneDeletedMetadata(path string, removed []deletedBinding) error {
	before, err := privacy.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta map[string]any
	if err := json.Unmarshal(before, &meta); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, item := range removed {
		if item.id != "" {
			ids[item.id] = true
		}
	}
	rows, ok := meta["instances"].([]any)
	if !ok {
		return nil
	}
	kept := make([]any, 0, len(rows))
	for _, value := range rows {
		row, _ := value.(map[string]any)
		id, _ := row["id"].(string)
		if !ids[id] {
			kept = append(kept, value)
		}
	}
	meta["instances"], meta["instance_count"] = kept, len(kept)
	if latest, _ := meta["latest_instance_id"].(string); ids[latest] {
		meta["latest_instance_id"] = ""
		if len(kept) > 0 {
			if row, ok := kept[len(kept)-1].(map[string]any); ok {
				meta["latest_instance_id"] = row["id"]
			}
		}
	}
	after, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return privacy.WriteRaw(path, after, 0o600)
}

// Installation filters in memory so staging/rollback keeps ownership of the
// live config. Even a deleted encrypted binding can be replaced without its key.
func LoadWritableBindingRoot(ctx context.Context, path string) (*RootConfig, error) {
	before, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	after, removed, err := filterDeletedBindings(ctx, path, before)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(after, before) {
		return LoadWritableRoot(path)
	}
	root, err := loadWritableRootData(path, after)
	if err == nil {
		root.sourceHash = sha256.Sum256(before)
		root.deletedBindings = removed
	}
	return root, err
}

// Finish staging cleanup only after the merged config and credentials exist.
func (root *RootConfig) PruneDeletedCredentials(path, configPath string) error {
	if len(root.deletedBindings) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	return pruneDeletedCredentials(path, data, configData, root.deletedBindings)
}
