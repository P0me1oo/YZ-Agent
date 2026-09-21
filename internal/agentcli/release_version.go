package agentcli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 历史两段版本补零；yz 修订按数字排序，同基线的无后缀正式版排在其后。
var releaseVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))?(?:-yz\.(0|[1-9][0-9]*))?$`)

// 固定结果码供面板翻译；本地命令保留具体原因，不上传命令输出。
type upgradeCheckError struct {
	code string
	err  error
}

func (e *upgradeCheckError) Error() string { return e.err.Error() }
func (e *upgradeCheckError) Unwrap() error { return e.err }

func upgradeErrorCode(err error) string {
	var check *upgradeCheckError
	if errors.As(err, &check) {
		return check.code
	}
	return "execution_failed"
}

func upgradeCheckFailure(code string, err error) error {
	return &upgradeCheckError{code: code, err: err}
}

func compareReleaseVersions(current, target string) (int, error) {
	a, b := releaseVersionPattern.FindStringSubmatch(current), releaseVersionPattern.FindStringSubmatch(target)
	if a == nil {
		return 0, upgradeCheckFailure("current_version_invalid", fmt.Errorf("无法识别当前版本 %q，已停止升级", current))
	}
	if b == nil {
		return 0, upgradeCheckFailure("latest_version_invalid", fmt.Errorf("无法识别目标版本 %q，已停止升级", target))
	}
	for i := 1; i <= 3; i++ {
		if a[i] == "" {
			a[i] = "0"
		}
		if b[i] == "" {
			b[i] = "0"
		}
		if cmp := compareVersionNumber(a[i], b[i]); cmp != 0 {
			return cmp, nil
		}
	}
	if a[4] == b[4] {
		return 0, nil
	}
	if a[4] == "" {
		return 1, nil
	}
	if b[4] == "" {
		return -1, nil
	}
	return compareVersionNumber(a[4], b[4]), nil
}

func compareVersionNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// 先固定正式版，再决定是否进入下载事务；调用方应持有安装锁。
func planLatestUpgrade(ctx context.Context, current string, latest func(context.Context) (string, error)) (target, message string, err error) {
	if _, err = compareReleaseVersions(current, current); err != nil {
		return "", "", err
	}
	target, err = latest(ctx)
	if err != nil {
		if upgradeErrorCode(err) == "latest_version_invalid" {
			return "", "", err
		}
		return "", "", upgradeCheckFailure("release_query_failed", fmt.Errorf("查询最新正式版失败，已停止升级：%w", err))
	}
	cmp, err := compareReleaseVersions(current, target)
	if err != nil {
		return "", "", err
	}
	if cmp == 0 {
		return "", "up_to_date", nil
	}
	if cmp > 0 {
		return "", "current_newer", nil
	}
	return target, "", nil
}
