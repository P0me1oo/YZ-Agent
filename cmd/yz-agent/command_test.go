package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentCommandEntrypoint(t *testing.T) {
	if os.Getenv("YZ_AGENT_ENTRYPOINT_TEST") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"yz-agent"}, os.Args[i+1:]...)
				break
			}
		}
		flag.CommandLine = flag.NewFlagSet("yz-agent", flag.ExitOnError)
		main()
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pathFile := filepath.Join(dir, "bin-dir")
	if err := os.WriteFile(pathFile, []byte("/opt/agent-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.yml")
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		failed bool
	}{
		{"default", nil, "yz-agent commands:", false},
		{"help", []string{"help"}, "yz-agent run [-c PATH]", false},
		{"version", []string{"version"}, "yz-agent v1.18.0", false},
		{"short-version", []string{"-v"}, "yz-agent v1.18.0", false},
		{"config", []string{"config", "bin-dir", "--path-file", pathFile}, "/opt/agent-test", false},
		{"run", []string{"run", "-c", missing}, "failed to load config:", true},
		{"legacy-start", []string{"-c", missing}, "failed to load config:", true},
		{"unknown", []string{"unknown-command"}, "unknown command: unknown-command", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(exe, append([]string{"-test.run=^TestAgentCommandEntrypoint$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "YZ_AGENT_ENTRYPOINT_TEST=1")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.failed || !strings.Contains(string(out), tc.want) {
				t.Fatalf("命令结果错误: err=%v, output=%s", err, out)
			}
		})
	}
}
