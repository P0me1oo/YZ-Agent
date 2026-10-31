package buildinfo

import (
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
)

func TestReleaseMetadataIsPinned(t *testing.T) {
	if XrayUpstreamTag != "v26.10.31" {
		t.Fatalf("XrayUpstreamTag = %q", XrayUpstreamTag)
	}
	if XrayUpstreamCommit != "a4a3dff5b20c2faae567b742c172c7b95e75c083" {
		t.Fatalf("XrayUpstreamCommit = %q", XrayUpstreamCommit)
	}
	if XrayForkVersion != "v26.11.0" {
		t.Fatalf("XrayForkVersion = %q", XrayForkVersion)
	}
	if XrayForkCommit != "b56aa7997c7ee6afe79664e13d64984da6a60f1c" {
		t.Fatalf("XrayForkCommit = %q", XrayForkCommit)
	}
	if got := "v" + core.Version(); got != XrayUpstreamTag {
		t.Fatalf("实际 Xray 核心版本 %q 与发布记录 %q 不一致", got, XrayUpstreamTag)
	}
	if core.YZForkVersion != XrayForkVersion {
		t.Fatalf("实际 YZ 核心版本 %q 与发布记录 %q 不一致", core.YZForkVersion, XrayForkVersion)
	}
	if SingBoxRequestedVersion != "v1.14.0" {
		t.Fatalf("SingBoxRequestedVersion = %q", SingBoxRequestedVersion)
	}
	if SingBoxResolvedVersion != "v1.15.1" {
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
