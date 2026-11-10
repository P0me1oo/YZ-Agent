package config

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func deletionFixture(t *testing.T, beforeResponse func()) (string, *atomic.Bool) {
	t.Helper()
	deleted := &atomic.Bool{}
	token := rand.Text()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MachineID int    `json:"machine_id"`
			Token     string `json:"token"`
			Nonce     string `json:"nonce"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/v2/server/machine/binding-status" || req.Token != token {
			w.WriteHeader(403)
			return
		}
		if req.MachineID != 117 || !deleted.Load() {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "machine_id": req.MachineID, "state": "active"}})
			return
		}
		if beforeResponse != nil {
			beforeResponse()
		}
		key := sha256.Sum256([]byte(token))
		mac := hmac.New(sha256.New, key[:])
		fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n117\n%s", req.Nonce)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "machine_id": 117, "state": "deleted", "nonce": req.Nonce, "proof": hex.EncodeToString(mac.Sum(nil))}})
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	data := fmt.Sprintf("custom_field: keep\ninstances:\n  - id: old\n    panel:\n      url: %q\n    machine:\n      machine_id: 117\n      token_env: OLD_BINDING_TEST\n    health_port: 65530\n    protected: deleted-machine-ciphertext\n  - id: new\n    panel:\n      url: %q\n    machine:\n      machine_id: 139\n      token_env: NEW_BINDING_TEST\n    kernel:\n      type: singbox\n    health_port: 65531\n    extra_field: retain\n", server.URL, server.URL)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.env"), []byte("OLD_BINDING_TEST="+token+"\nNEW_BINDING_TEST="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLD_BINDING_TEST", token)
	t.Setenv("NEW_BINDING_TEST", token)
	return path, deleted
}

func TestDeletedBindingCleanupPreservesOtherInstancesAndPendingData(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	before, _ := os.ReadFile(path)
	if changed, err := PruneDeletedMachines(context.Background(), path); err != nil || changed {
		t.Fatal("active binding changed", err)
	}
	if current, _ := os.ReadFile(path); !bytes.Equal(before, current) {
		t.Fatal("active config rewritten")
	}
	for _, name := range []string{"pending-traffic.json", "certificate.pem", "backup-config.yml"} {
		os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte("retain"), 0o600)
	}
	deleted.Store(true)
	if changed, err := PruneDeletedMachines(context.Background(), path); err != nil || !changed {
		t.Fatal("deleted binding retained", err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "117") || strings.Contains(string(after), "deleted-machine-ciphertext") || !strings.Contains(string(after), "extra_field: retain") || !strings.Contains(string(after), "custom_field: keep") {
		t.Fatal("wrong entries removed")
	}
	creds, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials.env"))
	if strings.Contains(string(creds), "OLD_BINDING_TEST") || !strings.Contains(string(creds), "NEW_BINDING_TEST") {
		t.Fatal("wrong credentials removed")
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil || len(instances) != 1 || instances[0].Machine.MachineID != 139 {
		t.Fatal("remaining binding cannot load", err)
	}
	if changed, err := PruneDeletedMachines(context.Background(), path); err != nil || changed {
		t.Fatal("repeated cleanup is not idempotent", err)
	}
	for _, name := range []string{"pending-traffic.json", "certificate.pem", "backup-config.yml"} {
		if data, _ := os.ReadFile(filepath.Join(filepath.Dir(path), name)); string(data) != "retain" {
			t.Fatal("persistent data changed")
		}
	}
}

func TestDeletedBindingKeepsSharedCredential(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	data, _ := os.ReadFile(path)
	data = bytes.ReplaceAll(data, []byte("NEW_BINDING_TEST"), []byte("OLD_BINDING_TEST"))
	os.WriteFile(path, data, 0o600)
	deleted.Store(true)
	if _, err := PruneDeletedMachines(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	creds, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials.env"))
	if !strings.Contains(string(creds), "OLD_BINDING_TEST=") {
		t.Fatal("shared credential removed")
	}
}

func TestDeletedBindingConcurrentRebindWins(t *testing.T) {
	var path string
	var once atomic.Bool
	fixturePath, deleted := deletionFixture(t, func() {
		if once.CompareAndSwap(false, true) {
			data, _ := os.ReadFile(path)
			os.WriteFile(path, append(data, []byte("rebound: true\n")...), 0o600)
		}
	})
	path = fixturePath
	deleted.Store(true)
	changed, err := PruneDeletedMachines(context.Background(), path)
	if changed || err == nil {
		t.Fatal("concurrent rebind overwritten")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "117") || !strings.Contains(string(data), "rebound: true") {
		t.Fatal("new config overwritten")
	}
}

func TestLastDeletedBindingBecomesExplicitIdleAndStagingDoesNotTouchLiveConfig(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	data, _ := os.ReadFile(path)
	data = bytes.Split(data, []byte("  - id: new"))[0]
	os.WriteFile(path, data, 0o600)
	deleted.Store(true)
	root, err := LoadWritableBindingRoot(context.Background(), path)
	if err != nil || !root.IsBindingIdle() {
		t.Fatal("staged cleanup failed", err)
	}
	if current, _ := os.ReadFile(path); !bytes.Equal(current, data) {
		t.Fatal("staging changed live config")
	}
	if changed, err := PruneDeletedMachines(context.Background(), path); err != nil || !changed {
		t.Fatal("last binding not removed", err)
	}
	root, err = LoadRoot(path)
	if err != nil || !root.IsBindingIdle() {
		t.Fatal("empty config cannot start idle", err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil || len(instances) != 0 {
		t.Fatal("idle mode recreated legacy binding", err)
	}
	if changed, err := PruneDeletedMachines(context.Background(), path); err != nil || changed {
		t.Fatal("idle config changed again", err)
	}
}

func TestDeletedBindingRetriesInterruptedMetadataCleanup(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	data, _ := os.ReadFile(path)
	var raw RootConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	oldID, _ := raw.Instances[0].AutoInstanceID()
	newID, _ := raw.Instances[1].AutoInstanceID()
	metaPath := filepath.Join(filepath.Dir(path), "install-meta.json")
	os.WriteFile(metaPath, []byte("broken-json"), 0o600)
	deleted.Store(true)
	if changed, err := PruneDeletedMachines(context.Background(), path); !changed || err == nil {
		t.Fatal("metadata failure not reported")
	}
	meta, _ := json.Marshal(map[string]any{"version": "test", "latest_instance_id": oldID, "instance_count": 2, "instances": []any{map[string]any{"id": oldID}, map[string]any{"id": newID}}})
	os.WriteFile(metaPath, meta, 0o600)
	// Completion is local; panel no longer needs to confirm the removed binding.
	deleted.Store(false)
	if changed, err := PruneDeletedMachines(context.Background(), path); changed || err != nil {
		t.Fatal("interrupted cleanup not recovered", err)
	}
	current, _ := os.ReadFile(metaPath)
	var parsed map[string]any
	json.Unmarshal(current, &parsed)
	if parsed["latest_instance_id"] != newID || parsed["instance_count"] != float64(1) || strings.Contains(string(current), oldID) {
		t.Fatal("metadata still references deleted binding")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".binding-cleanup.json")); !os.IsNotExist(err) {
		t.Fatal("completion journal retained")
	}
}

func TestLegacyDeletedBindingCanBecomeIdleWithoutItsDecryptionKey(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	data, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(data, &doc)
	legacy, _ := yaml.Marshal(doc["instances"].([]any)[0])
	os.WriteFile(path, legacy, 0o600)
	deleted.Store(true)
	if changed, err := PruneDeletedMachines(context.Background(), path); !changed || err != nil {
		t.Fatal("legacy cleanup failed", err)
	}
	root, err := LoadRoot(path)
	if err != nil || !root.IsBindingIdle() || root.HealthPort != 65530 {
		t.Fatal("legacy idle config invalid", err)
	}
}

func TestDeletedBindingDoesNotRemoveAnOverriddenRuntimeIdentity(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	before, _ := os.ReadFile(path)
	deleted.Store(true)
	t.Setenv("MACHINE_ID", "139")
	if changed, err := PruneDeletedMachines(context.Background(), path); changed || err != nil {
		t.Fatal("overridden identity was pruned", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("disk binding changed")
	}
}

func TestDeletedBindingPreservesUnrelatedProtectedEnvelope(t *testing.T) {
	path, deleted := deletionFixture(t, nil)
	data, _ := os.ReadFile(path)
	data = bytes.ReplaceAll(data, []byte("extra_field: retain"), []byte("extra_field: retain\n    protected: unrelated-ciphertext"))
	os.WriteFile(path, data, 0o600)
	deleted.Store(true)
	if changed, err := PruneDeletedMachines(context.Background(), path); !changed || err != nil {
		t.Fatal("cleanup failed", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Contains(after, []byte("protected: unrelated-ciphertext")) || !bytes.Contains(after, []byte("NEW_BINDING_TEST")) {
		t.Fatal("unrelated protected binding damaged")
	}
}
