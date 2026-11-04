package nlog

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestLogPolicySilencesAndRestoresAllOutputsFromPanel(t *testing.T) {
	logMu.RLock()
	writer, min, color := logWriter, logMin, logColor
	logMu.RUnlock()
	defer Init(writer, min, color)
	defer ConfigureLogPolicies(nil)
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	var output bytes.Buffer
	Init(&output, slog.LevelDebug, false)
	count := 0
	stop := Subscribe(key, func(Event) { count++ })
	defer stop()
	logger := Scoped(key, 7, "node:7")
	write := func() {
		logger.Debug("tracker: 827 conns, 381 users online")
		logger.Info("started, 937 users")
		logger.Warn("node unavailable", "node_id", 7)
		logger.Error("node failed")
		Publish(key, Event{Source: "xray", Level: "error", Event: "xray.crash", Message: "core failed"})
		WriteRuntime("core connection accepted")
	}
	write()
	if output.Len() != 0 || count != 0 {
		t.Fatal("startup before policy must remain silent")
	}
	ApplyLogPolicy(key, false)
	write()
	if output.Len() == 0 || count != 5 {
		t.Fatal("explicit normal policy must retain existing logs")
	}
	output.Reset()
	count = 0
	ApplyLogPolicy(key, true)
	Init(&output, slog.LevelDebug, false)
	write()
	if !LogsDisabled() || output.Len() != 0 || count != 0 {
		t.Fatal("local level changes must not override panel privacy")
	}
	ApplyLogPolicy(key, false)
	write()
	if LogsDisabled() || output.Len() == 0 || count != 5 {
		t.Fatal("panel must restore console and event outputs")
	}
	output.Reset()
	count = 0
	ConfigureLogPolicies([]string{key})
	write()
	if !LogsDisabled() || output.Len() != 0 || count != 0 {
		t.Fatal("reload must wait for the panel policy")
	}
	ApplyLogPolicy(key, false)
	write()
	if LogsDisabled() || output.Len() == 0 || count != 5 {
		t.Fatal("reload must honor the restored panel policy")
	}
}

func TestLogPolicyWaitsForAllIdentities(t *testing.T) {
	defer ConfigureLogPolicies(nil)
	one, two := t.Name()+"/one", t.Name()+"/two"
	ConfigureLogPolicies([]string{one, two})
	ApplyLogPolicy(one, false)
	ApplyLogPolicy("unrelated", false)
	if !LogsDisabled() {
		t.Fatal("unknown identity must remain silent")
	}
	ApplyLogPolicy(two, false)
	if LogsDisabled() {
		t.Fatal("all identities explicitly allow logs")
	}
	ApplyLogPolicy(two, true)
	ApplyLogPolicy(one, false)
	if !LogsDisabled() {
		t.Fatal("one disabled identity must protect shared outputs")
	}
	ApplyLogPolicy(two, false)
	if LogsDisabled() {
		t.Fatal("shared outputs must recover after all identities allow logs")
	}
}
