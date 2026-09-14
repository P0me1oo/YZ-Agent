#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST_ROOT=$(mktemp -d)
INSTALLER_LIBRARY="$TEST_ROOT/install-library.sh"

cleanup() {
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

# 安装器通过标准输入运行，不能添加仅依赖 BASH_SOURCE 的入口保护。
# 测试副本只移除最后的 main 调用，用于直接验证服务文件渲染函数。
sed '/^main "$@"$/d' "$REPO_ROOT/install.sh" >"$INSTALLER_LIBRARY"
# shellcheck source=/dev/null
source "$INSTALLER_LIBRARY"
trap cleanup EXIT

TMP_DIR="$TEST_ROOT/openrc"
mkdir -p "$TMP_DIR"
SERVICE_MANAGER="openrc"
render_service
sh -n "$TMP_DIR/service"
grep -F 'supervisor=supervise-daemon' "$TMP_DIR/service" >/dev/null
grep -F 'respawn_max=0' "$TMP_DIR/service" >/dev/null
grep -F 'retry="TERM/150/KILL/5"' "$TMP_DIR/service" >/dev/null
grep -F 'key=${line%%=*}' "$TMP_DIR/service" >/dev/null
grep -F 'export "${key}=${value}"' "$TMP_DIR/service" >/dev/null
if grep -F '. /etc/yz-agent/credentials.env' "$TMP_DIR/service" >/dev/null; then
    echo "OpenRC service must not execute credentials.env as a shell script" >&2
    exit 1
fi

TMP_DIR="$TEST_ROOT/systemd"
mkdir -p "$TMP_DIR"
SERVICE_MANAGER="systemd"
render_service
grep -F 'EnvironmentFile=-/etc/yz-agent/credentials.env' "$TMP_DIR/service" >/dev/null
grep -F 'Restart=always' "$TMP_DIR/service" >/dev/null
grep -F 'TimeoutStopSec=150s' "$TMP_DIR/service" >/dev/null

echo "installer service-manager tests passed"
