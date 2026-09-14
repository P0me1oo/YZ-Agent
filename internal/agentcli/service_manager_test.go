package agentcli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fakeLookPath(commands ...string) func(string) (string, error) {
	available := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		available[command] = struct{}{}
	}
	return func(command string) (string, error) {
		if _, ok := available[command]; ok {
			return "/sbin/" + command, nil
		}
		return "", errors.New("not found")
	}
}

func TestDetectServiceManagerPrefersRunningSystemd(t *testing.T) {
	t.Parallel()

	manager, err := detectServiceManagerWith(
		fakeLookPath("systemctl", "openrc-run", "rc-service", "rc-update", "supervise-daemon"),
		func(path string) bool { return path == systemdRuntimeDirectory },
	)
	if err != nil {
		t.Fatal(err)
	}
	if manager != serviceManagerSystemd {
		t.Fatalf("manager = %q, want %q", manager, serviceManagerSystemd)
	}
}

func TestDetectServiceManagerUsesOpenRCWithoutRunningSystemd(t *testing.T) {
	t.Parallel()

	manager, err := detectServiceManagerWith(
		fakeLookPath("systemctl", "openrc-run", "rc-service", "rc-update", "supervise-daemon"),
		func(string) bool { return false },
	)
	if err != nil {
		t.Fatal(err)
	}
	if manager != serviceManagerOpenRC {
		t.Fatalf("manager = %q, want %q", manager, serviceManagerOpenRC)
	}
}

func TestDetectServiceManagerRejectsIncompleteOpenRC(t *testing.T) {
	t.Parallel()

	_, err := detectServiceManagerWith(
		fakeLookPath("rc-service", "rc-update"),
		func(string) bool { return false },
	)
	if err == nil {
		t.Fatal("detectServiceManagerWith() error = nil, want unsupported manager error")
	}
}

func TestServiceCommandForOpenRC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
		rest   []string
		want   serviceCommandSpec
	}{
		{name: "status", action: "status", want: serviceCommandSpec{name: "rc-service", args: []string{serviceName, "status"}}},
		{name: "restart", action: "restart", want: serviceCommandSpec{name: "rc-service", args: []string{serviceName, "restart"}}},
		{name: "enable", action: "enable", want: serviceCommandSpec{name: "rc-update", args: []string{"add", serviceName, "default"}}},
		{name: "disable", action: "disable", want: serviceCommandSpec{name: "rc-update", args: []string{"del", serviceName, "default"}}},
		{name: "default logs", action: "logs", want: serviceCommandSpec{name: "tail", args: []string{"-f", openRCLogPath}}},
		{name: "bounded logs", action: "logs", rest: []string{"-n", "50"}, want: serviceCommandSpec{name: "tail", args: []string{"-n", "50", openRCLogPath}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := serviceCommandFor(serviceManagerOpenRC, tt.action, false, tt.rest)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("serviceCommandFor() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestServiceCommandForSystemdUsesSudoForNonRootCaller(t *testing.T) {
	t.Parallel()

	got, err := serviceCommandFor(serviceManagerSystemd, "restart", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := serviceCommandSpec{name: "sudo", args: []string{"systemctl", "restart", systemdServiceName}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceCommandFor() = %#v, want %#v", got, want)
	}
}

func TestNormalizeOpenRCState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		output string
		err    error
		want   string
	}{
		{output: " * status: started", want: "active"},
		{output: " * status: stopped", err: errors.New("exit 3"), want: "inactive"},
		{output: " * status: crashed", err: errors.New("exit 32"), want: "failed"},
		{err: errors.New("missing service"), want: "unknown"},
	}
	for _, tt := range tests {
		if got := normalizeOpenRCState(tt.output, tt.err); got != tt.want {
			t.Errorf("normalizeOpenRCState(%q) = %q, want %q", tt.output, got, tt.want)
		}
	}
}

func TestOpenRCServiceDefinition(t *testing.T) {
	t.Parallel()

	path, content, mode, err := serviceDefinitionFor(serviceManagerOpenRC)
	if err != nil {
		t.Fatal(err)
	}
	if path != openRCServiceFilePath || mode != 0o755 {
		t.Fatalf("path/mode = %q/%#o, want %q/%#o", path, mode, openRCServiceFilePath, 0o755)
	}
	script := string(content)
	for _, required := range []string{
		"#!/sbin/openrc-run",
		"supervisor=supervise-daemon",
		"respawn_delay=5",
		"respawn_max=0",
		"retry=\"TERM/150/KILL/5\"",
		"key=${line%%=*}",
		"export \"${key}=${value}\"",
		"checkpath -f -m 0640",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("OpenRC service definition is missing %q", required)
		}
	}
	if strings.Contains(script, "source ") || strings.Contains(script, ". "+defaultCredentialsPath) {
		t.Fatal("OpenRC service definition must not execute credentials.env as a shell script")
	}
}

func TestSystemdServiceDefinitionAllowsFinalReports(t *testing.T) {
	_, content, _, err := serviceDefinitionFor(serviceManagerSystemd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "TimeoutStopSec=150s") {
		t.Fatal("systemd 停止超时必须覆盖进程的两分钟退出等待")
	}
}
