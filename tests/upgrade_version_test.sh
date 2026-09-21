#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source <(sed '/^main "$@"$/d' "$REPO_ROOT/install.sh")
# 测试只模拟查询和现有程序，不下载附件、不操作系统服务。
trap - EXIT ERR
log_info() { printf '%s\n' "$*"; }
log_error() { printf '%s\n' "$*" >&2; }
fake_node() { printf 'yz-agent %s\n' "$CURRENT_TEST_VERSION"; }
curl() {
    [ "$QUERY_FAIL" = 0 ] || return 22
    printf 'https://example.test/releases/tag/%s' "$LATEST_TEST_VERSION"
}
PREVIOUS_BINARY_PATH=fake_node

check_case() {
    CURRENT_TEST_VERSION="$1" LATEST_TEST_VERSION="$2" QUERY_FAIL=0
    RELEASE_VERSION=latest
    check_latest_upgrade
    [ "$UPGRADE_SKIPPED" = "$3" ]
    if [ "$3" = 0 ]; then
        [ "$RELEASE_VERSION" = "$2" ]
        [ "$UPGRADE_FROM_RELEASE" = 1 ]
    else
        [ "$UPGRADE_FROM_RELEASE" = 0 ]
    fi
}
check_case v1.16.0 v1.16.1 0
check_case v1.16.1 v1.16.1 1
check_case 1.16.1 v1.16.1 1
check_case v1.17.0 v1.16.1 1
check_case v1.13-yz.9 v1.13-yz.10 0
check_case v1.13-yz.10 v1.13-yz.9 1
check_case v1.13-yz.24 v1.13.1 0
check_case v0.1.0-yz.1 v1.16.1 0
check_case v1.13 v1.13.0 1
for invalid in dev unknown v01.2.3 v1.16.1-beta.1; do
    CURRENT_TEST_VERSION="$invalid" RELEASE_VERSION=latest
    if check_latest_upgrade; then exit 1; fi
    CURRENT_TEST_VERSION=v1.16.0 LATEST_TEST_VERSION="$invalid"
    if check_latest_upgrade; then exit 1; fi
done
LATEST_TEST_VERSION=v1.16.1 QUERY_FAIL=1
if check_latest_upgrade; then exit 1; fi
# 显式历史回滚不依赖 latest 查询。
RELEASE_VERSION=v1.13-yz.24
check_latest_upgrade
[ "$UPGRADE_SKIPPED" = 0 ]
[ "$RELEASE_VERSION" = v1.13-yz.24 ]
# 同版本重复执行不进入下载和服务操作。
detect_current_state() { CURRENT_STATE=installed; }
install_dependencies() { echo '无需升级时不应安装依赖' >&2; exit 1; }
load_health_port_from_config() { echo '不应进入安装事务' >&2; exit 1; }
CURRENT_TEST_VERSION=v1.16.1 LATEST_TEST_VERSION=v1.16.1 QUERY_FAIL=0
RELEASE_VERSION=latest
perform_upgrade
perform_upgrade
# 缺失安装及版本命令失败必须停止，不能转为首次安装。
detect_current_state() { CURRENT_STATE=fresh; }
if perform_upgrade; then exit 1; fi
fake_node() { return 1; }
if check_latest_upgrade; then exit 1; fi
echo '升级版本检查测试通过'
