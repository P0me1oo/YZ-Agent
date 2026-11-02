package agentcli

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestConfigInitUsesPrivateTokenInput(t *testing.T) {
	for _, ending := range []string{"", "\n", "\r\n"} {
		t.Run(strings.ReplaceAll(ending, "\n", "LF"), func(t *testing.T) {
			dir := t.TempDir()
			path, credentials := filepath.Join(dir, "config.yml"), filepath.Join(dir, "credentials.env")
			token := rand.Text()
			args := kernelInitArgs(path, "--credentials-out", credentials, "--token-stdin")
			if err := runConfigInitWithInput(args, strings.NewReader(token+ending)); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(credentials)
			if err != nil {
				t.Fatal(err)
			}
			_, saved, ok := strings.Cut(strings.TrimSpace(string(data)), "=")
			if !ok || saved != token {
				t.Fatal("标准输入中的凭据未正确保存")
			}
			configData, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(configData), token) {
				t.Fatal("主配置不应包含明文凭据")
			}
		})
	}
}

func TestConfigInitRejectsInvalidPrivateTokenInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		input io.Reader
		extra []string
	}{
		{"read_failure", iotest.ErrReader(io.ErrUnexpectedEOF), nil},
		{"too_large", strings.NewReader(strings.Repeat("x", maxInstallTokenBytes+1)), nil},
		{"multiple_lines", strings.NewReader("test-only\nsecond-entry"), nil},
		{"null_byte", strings.NewReader("test-only\x00entry"), nil},
		{"conflicting_options", strings.NewReader(rand.Text()), []string{"--token", rand.Text()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			args := append(kernelInitArgs(path, "--token-stdin"), test.extra...)
			if err := runConfigInitWithInput(args, test.input); err == nil {
				t.Fatal("无效凭据输入应在写文件前被拒绝")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("凭据输入失败时不应写入配置")
			}
		})
	}
}

func TestConfigInitPrivateInputCapabilityHasNoSideEffects(t *testing.T) {
	if err := runConfigInitWithInput([]string{"--supports-token-stdin"}, iotest.ErrReader(io.ErrUnexpectedEOF)); err != nil {
		t.Fatal(err)
	}
}
