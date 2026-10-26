package main

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestConfigureLogPoliciesCoversMachineAndLegacyNodes(t *testing.T) {
	const panelURL = "https://log-policy.example.invalid"
	instances := []*config.Config{
		{Panel: config.PanelConfig{URL: panelURL}, Machine: &config.MachineConfig{MachineID: 7}},
		{Panel: config.PanelConfig{URL: panelURL}, Nodes: []config.NodeEntry{{NodeID: 11}, {NodeID: 12}}},
	}
	configureLogPolicies(instances)
	defer nlog.ConfigureLogPolicies(nil)
	keys := []string{nlog.LogPolicyKey(panelURL, 7, 0), nlog.LogPolicyKey(panelURL, 0, 11), nlog.LogPolicyKey(panelURL, 0, 12)}
	for index, key := range keys {
		if !nlog.LogsDisabled() {
			t.Fatal("startup must wait for every bound identity")
		}
		nlog.ApplyLogPolicy(key, false)
		if index == len(keys)-1 && nlog.LogsDisabled() {
			t.Fatal("matching panel policies did not enable normal output")
		}
	}
	nlog.ApplyLogPolicy(keys[0], true)
	configureLogPolicies(instances)
	for _, key := range keys {
		nlog.ApplyLogPolicy(key, false)
	}
	config.InitLogger(config.LogConfig{Level: "debug", Output: "stdout"})
	if !nlog.LogsDisabled() {
		t.Fatal("reload and local debug level must not remove permanent policy")
	}
}
