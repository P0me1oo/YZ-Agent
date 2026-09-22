package buildinfo

import (
	"strings"
	"testing"
)

func TestReleaseMetadataIsPinned(t *testing.T) {
	if XrayUpstreamTag != "v26.9.9" {
		t.Fatalf("XrayUpstreamTag = %q", XrayUpstreamTag)
	}
	if XrayUpstreamCommit != "52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120" {
		t.Fatalf("XrayUpstreamCommit = %q", XrayUpstreamCommit)
	}
	if XrayForkVersion != "v26.8.1" {
		t.Fatalf("XrayForkVersion = %q", XrayForkVersion)
	}
	if XrayForkCommit != "f242ad6931521c1d56245e3566dcf7bc11b9571c" {
		t.Fatalf("XrayForkCommit = %q", XrayForkCommit)
	}
	if SingBoxRequestedVersion != "v1.14.0" {
		t.Fatalf("SingBoxRequestedVersion = %q", SingBoxRequestedVersion)
	}
	if SingBoxResolvedVersion != "v1.14.0-yz.2" {
		t.Fatalf("SingBoxResolvedVersion = %q", SingBoxResolvedVersion)
	}
}

func TestReportContainsForkAndDependencyIdentity(t *testing.T) {
	report := Report("xboard-node", "v1.13-yz.1", "2026-07-25T00:00:00Z", "abc1234")
	for _, want := range []string{
		"xboard-node v1.13-yz.1",
		XrayForkVersion,
		XrayUpstreamCommit,
		XrayForkCommit,
		SingBoxRequestedVersion,
		SingBoxResolvedVersion,
		SingBoxUpstreamCommit,
		SingBoxForkCommit,
		ShadowsocksUpstreamVersion,
		ShadowsocksUpstreamCommit,
		AnyTLSUpstreamVersion,
		AnyTLSUpstreamCommit,
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q does not contain %q", report, want)
		}
	}
}
