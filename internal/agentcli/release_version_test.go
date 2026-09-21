package agentcli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlanLatestUpgrade(t *testing.T) {
	for _, tc := range []struct {
		current, latest, message string
		wantUpgrade, wantError   bool
	}{
		{"v1.16.0", "v1.16.1", "", true, false},
		{"v1.16.1", "v1.16.1", "up_to_date", false, false},
		{"1.16.1", "v1.16.1", "up_to_date", false, false},
		{"v1.17.0", "v1.16.1", "current_newer", false, false},
		{"v1.13-yz.9", "v1.13-yz.10", "", true, false},
		{"v1.13-yz.24", "v1.13.1", "", true, false},
		{"v0.1.0-yz.1", "v1.16.1", "", true, false},
		{"v1.13-yz.10", "v1.13-yz.9", "current_newer", false, false},
		{"v1.13-yz.24", "v1.13.0", "", true, false},
		{"v1.13", "v1.13.0", "up_to_date", false, false},
		{"dev", "v1.16.1", "当前版本", false, true},
		{"1410e37", "v1.16.1", "当前版本", false, true},
		{"v1.16.0", "v1.17.0-beta.1", "目标版本", false, true},
		{"v1.16.0", "v01.17.0", "目标版本", false, true},
		{"v1.16.0", "", "目标版本", false, true},
	} {
		t.Run(tc.current+"/"+tc.latest, func(t *testing.T) {
			calls := 0
			target, message, err := planLatestUpgrade(context.Background(), tc.current, func(context.Context) (string, error) {
				calls++
				return tc.latest, nil
			})
			if (err != nil) != tc.wantError || (target != "") != tc.wantUpgrade || calls > 1 {
				t.Fatalf("target=%q message=%q err=%v calls=%d", target, message, err, calls)
			}
			if err != nil {
				message = err.Error()
			}
			if !strings.Contains(message, tc.message) {
				t.Fatalf("message=%q", message)
			}
		})
	}
	errQuery := errors.New("HTTP 503")
	target, _, err := planLatestUpgrade(context.Background(), "v1.16.0", func(context.Context) (string, error) { return "", errQuery })
	if target != "" || !errors.Is(err, errQuery) || !strings.Contains(err.Error(), "查询最新正式版失败") {
		t.Fatalf("target=%q err=%v", target, err)
	}
}
