package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
	blog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func TestRuntimeLogNodeAttribution(t *testing.T) {
	var got []nlog.Event
	stop := nlog.Subscribe("singbox-test", func(event nlog.Event) { got = append(got, event) })
	defer stop()
	writer := runtimeLogWriter{scope: "singbox-test", nodeID: 9}
	writer.WriteMessage(blog.LevelWarn, "listener failed")
	if len(got) != 1 || got[0].NodeID != 9 || got[0].Source != "singbox" || got[0].Level != "warn" {
		t.Fatal("runtime attribution lost")
	}
}

func TestRunningLogFactoryEncryptsAndReportsWhenPrivacyIsEnabled(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	scope := t.Name()
	nlog.ConfigureLogPolicies([]string{scope})
	nlog.RegisterLogScope(scope, scope, filepath.Join(dir, "runtime.log.enc"), state)
	nlog.ApplyLogPolicy(scope, false)
	t.Cleanup(func() { nlog.ConfigureLogPolicies(nil); nlog.Init(os.Stdout, slog.LevelInfo, false) })
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelInfo, false)
	events := []nlog.Event{}
	stop := nlog.Subscribe(scope, func(event nlog.Event) { events = append(events, event) })
	defer stop()
	factory, err := blog.New(blog.Options{Context: t.Context(), Options: option.LogOptions{Output: os.DevNull}, Observable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	factory.(blog.ObservableFactory).AttachPlatformWriter(runtimeLogWriter{scope: scope, nodeID: 9, factory: factory})
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	factory.Logger().Error("normal singbox error")
	if console.Len() == 0 {
		t.Fatal("normal core output missing")
	}
	console.Reset()
	nlog.ApplyLogPolicy(scope, true)
	factory.Logger().Error("private singbox connection")
	if console.Len() != 0 || len(events) != 2 || events[1].NodeID != 9 {
		t.Fatal("core factory lost private logs or exposed plaintext")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.log.enc"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private singbox")) || !strings.Contains(decoded.String(), "private singbox connection") {
		t.Fatal("singbox runtime log not encrypted")
	}
}

func TestRuntimeLogPrivacyDisablesRawLogsWithoutChangingUsers(t *testing.T) {
	nlog.ConfigureLogPolicies([]string{t.Name()})
	defer nlog.ConfigureLogPolicies(nil)
	nlog.ApplyLogPolicy(t.Name(), false)
	settings := config.KernelConfig{LogLevel: "debug"}
	node := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 10001, Cipher: "aes-128-gcm"}
	normal, err := buildConfig(settings, node, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	nlog.ApplyLogPolicy(t.Name(), true)
	private, err := buildConfig(settings, node, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	if private["log"].(M)["output"] != os.DevNull || !reflect.DeepEqual(normal["inbounds"], private["inbounds"]) {
		t.Fatal("privacy must disable raw logs without changing authentication")
	}
	data, err := json.Marshal(private["log"])
	if err != nil {
		t.Fatal(err)
	}
	var options option.LogOptions
	if err := json.Unmarshal(data, &options); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelDebug, false)
	defer nlog.Init(os.Stdout, slog.LevelInfo, false)
	factory, err := blog.New(blog.Options{Context: context.Background(), Options: options, Observable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	factory.(blog.ObservableFactory).AttachPlatformWriter(runtimeLogWriter{scope: "singbox-privacy-test", nodeID: 9, factory: factory})
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	factory.Logger().Error("937 users online")
	if console.Len() != 0 {
		t.Fatal("raw sing-box console output bypassed privacy")
	}
	count := 0
	stop := nlog.Subscribe("singbox-privacy-test", func(nlog.Event) { count++ })
	defer stop()
	writer := runtimeLogWriter{scope: "singbox-privacy-test", nodeID: 9}
	writer.WriteMessage(blog.LevelWarn, "937 users online")
	if count != 0 {
		t.Fatal("raw sing-box logs bypassed privacy")
	}
	nlog.ApplyLogPolicy(t.Name(), false)
	writer.WriteMessage(blog.LevelWarn, "restored")
	if count != 1 || console.Len() == 0 {
		t.Fatal("sing-box console and events did not resume")
	}
}

func TestRunningLogFactoryStopsOutputWhenPanelPolicyChanges(t *testing.T) {
	nlog.ConfigureLogPolicies([]string{t.Name()})
	defer nlog.ConfigureLogPolicies(nil)
	nlog.ApplyLogPolicy(t.Name(), false)
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelDebug, false)
	defer nlog.Init(os.Stdout, slog.LevelInfo, false)
	factory, err := blog.New(blog.Options{Context: context.Background(), Options: option.LogOptions{Output: os.DevNull}, Observable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	factory.(blog.ObservableFactory).AttachPlatformWriter(runtimeLogWriter{scope: t.Name(), factory: factory})
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	factory.Logger().Error("normal output")
	if console.Len() == 0 {
		t.Fatal("normal core output missing")
	}
	console.Reset()
	nlog.ApplyLogPolicy(t.Name(), true)
	factory.Logger().Error("937 users online")
	if console.Len() != 0 {
		t.Fatal("running core bypassed disabled policy")
	}
}
