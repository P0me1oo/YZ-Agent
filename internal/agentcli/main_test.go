package agentcli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

func TestResolveDownloadURLUsesYZboardNodeRelease(t *testing.T) {
	t.Parallel()

	got := resolveDownloadURL("xboard-node-linux-amd64", "v1.13-yz.2")
	want := "https://github.com/P0me1oo/YZ-Agent/releases/download/v1.13-yz.2/xboard-node-linux-amd64"
	if got != want {
		t.Fatalf("resolveDownloadURL() = %q, want %q", got, want)
	}
}

func TestRunDoctorRequiresTimeSubcommand(t *testing.T) {
	if err := runDoctor(nil); err == nil || !strings.Contains(err.Error(), "doctor time") {
		t.Fatalf("runDoctor(nil) error = %v", err)
	}
	if err := runDoctor([]string{"network"}); err == nil || !strings.Contains(err.Error(), "unknown doctor command") {
		t.Fatalf("runDoctor(unknown) error = %v", err)
	}
}

func TestLoadDoctorTimeConfigWithoutPanelCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`
time_sync:
  enabled: false
  servers:
    - ntp.example.com
  interval: 120
  timeout: 4
  warn_offset: 6
  error_offset: 16
  critical_offset: 26
instances:
  - panel:
      url: https://panel.example.com
      token_env: TOKEN_NOT_AVAILABLE_IN_SHELL
      node_id: 1
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, enabled, err := loadDoctorTimeConfig(path)
	if err != nil {
		t.Fatalf("loadDoctorTimeConfig() error = %v", err)
	}
	if enabled {
		t.Fatal("configured enabled = true, want false")
	}
	if cfg.Interval != 120 || len(cfg.Servers) != 1 || cfg.Servers[0] != "ntp.example.com" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestPrintTimeDoctorText(t *testing.T) {
	checkedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	result := timeDoctorResult{
		ConfiguredEnabled: true,
		Servers:           []string{"time.cloudflare.com", "time.google.com"},
		Probe: timesync.Snapshot{
			Enabled:     true,
			Status:      timesync.StatusWarning,
			OffsetMS:    8300,
			Source:      "time.cloudflare.com",
			LastSuccess: &checkedAt,
		},
	}
	var output bytes.Buffer
	if err := printTimeDoctor(&output, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"configured: enabled", "status:     warning", "offset:     8300 ms", "time.cloudflare.com"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestVerifyReleaseChecksum(t *testing.T) {
	t.Parallel()

	payload := []byte("yzboard-node release artifact")
	artifact := "xboard-node-linux-amd64"
	path := filepath.Join(t.TempDir(), artifact)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	checksums := []byte(fmt.Sprintf("%x  %s\n", sum, artifact))
	if err := verifyReleaseChecksum(path, artifact, checksums); err != nil {
		t.Fatalf("verifyReleaseChecksum() error = %v", err)
	}
}

func TestVerifyReleaseChecksumRejectsMismatch(t *testing.T) {
	t.Parallel()

	artifact := "xbctl-linux-arm64"
	path := filepath.Join(t.TempDir(), artifact)
	if err := os.WriteFile(path, []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	checksums := []byte(fmt.Sprintf("%064x  %s\n", 0, artifact))
	if err := verifyReleaseChecksum(path, artifact, checksums); err == nil {
		t.Fatal("verifyReleaseChecksum() error = nil, want checksum mismatch")
	}
}

func TestInstalledVersion(t *testing.T) {
	t.Parallel()

	report := []byte("xboard-node v1.13-yz.2 (built 2026-07-25T00:00:00Z, commit abc1234)\nother details")
	if got := installedVersion("latest", report); got != "v1.13-yz.2" {
		t.Fatalf("installedVersion(latest) = %q", got)
	}
	if got := installedVersion("v1.13-yz.2", []byte("invalid report")); got != "v1.13-yz.2" {
		t.Fatalf("installedVersion(fixed) = %q", got)
	}
}
