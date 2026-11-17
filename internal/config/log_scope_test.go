package config

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

func TestRuntimeLogKeysAreFetchedAgainInFreshProcess(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, _, _ := privacyFixtureWithPolicy(t, &policy)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil || len(instances) != 1 {
		t.Fatal("normalization failed", err)
	}
	state := instances[0].RuntimeLogState()
	if state == nil {
		t.Fatal("panel key not retained in memory")
	}
	policyKey := nlog.LogPolicyKey(instances[0].Panel.URL, instances[0].Machine.MachineID, 0)
	nlog.ConfigureLogPolicies([]string{policyKey})
	t.Cleanup(func() { nlog.ConfigureLogPolicies(nil) })
	instances[0].RegisterRuntimeLogs()
	if !nlog.PolicyReady(policyKey) || !nlog.LogsDisabled() {
		t.Fatal("authenticated startup policy did not enable encrypted logging")
	}
	logPath := filepath.Join(instances[0].Kernel.ConfigDir, "runtime.log.enc")
	if _, err := privacy.NewLogWriter(logPath, state).Write([]byte("cold runtime recovery marker\n")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{"private restart", true}, {"closed before restart", false}, {"enabled again", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy.Store(test.enabled)
			cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeLogColdProcessHelper$")
			cmd.Env = append(os.Environ(), "YZ_RUNTIME_LOG_CONFIG_TEST="+path,
				"YZ_RUNTIME_LOG_PRIVATE_TEST="+strconv.FormatBool(test.enabled))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cold log recovery failed: %v\n%s", err, out)
			}
			after, err := os.ReadFile(logPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("restart changed historical ciphertext", err)
			}
		})
	}
}

func TestRuntimeLogColdProcessHelper(t *testing.T) {
	path := os.Getenv("YZ_RUNTIME_LOG_CONFIG_TEST")
	if path == "" {
		return
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil || len(instances) != 1 {
		t.Fatal("configuration recovery failed", err)
	}
	state := instances[0].RuntimeLogState()
	if state == nil {
		t.Fatal("restart did not fetch machine key")
	}
	policyKey := nlog.LogPolicyKey(instances[0].Panel.URL, instances[0].Machine.MachineID, 0)
	nlog.ConfigureLogPolicies([]string{policyKey})
	t.Cleanup(func() { nlog.ConfigureLogPolicies(nil) })
	instances[0].RegisterRuntimeLogs()
	if !nlog.LogsReady(instances[0].LogScope()) || nlog.LogsDisabled() != (os.Getenv("YZ_RUNTIME_LOG_PRIVATE_TEST") == "true") {
		t.Fatal("startup logging did not honor the recovered panel policy")
	}
	raw, err := os.ReadFile(filepath.Join(instances[0].Kernel.ConfigDir, "runtime.log.enc"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if decoded.String() != "cold runtime recovery marker\n" {
		t.Fatal("historical runtime log not recovered")
	}
}
