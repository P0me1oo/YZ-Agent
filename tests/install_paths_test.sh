#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST_PARENT=$(cd "${TMPDIR:-/tmp}" && pwd -P)
TEST_ROOT=$(mktemp -d "$TEST_PARENT/yzboard-install-paths.XXXXXX")
TEST_BIN_PARENT=$(cd "${YZ_TEST_BIN_PARENT:-$TEST_PARENT}" && pwd -P)
TEST_BIN_ROOT=$(mktemp -d "$TEST_BIN_PARENT/yzboard-install-binaries.XXXXXX")
cleanup() {
    local exit_code=$?
    if [ "$exit_code" -ne 0 ] && [ -n "${case_name:-}" ] && [ -f "$TEST_ROOT/$case_name.log" ]; then
        printf '安装器场景失败：%s\n' "$case_name" >&2
        cat "$TEST_ROOT/$case_name.log" >&2
    fi
    case "$TEST_ROOT" in
        "$TEST_PARENT"/yzboard-install-paths.*) rm -rf -- "$TEST_ROOT" ;;
        *) echo "拒绝清理范围外的测试目录" >&2; return 1 ;;
    esac
    case "$TEST_BIN_ROOT" in
        "$TEST_BIN_PARENT"/yzboard-install-binaries.*) rm -rf -- "$TEST_BIN_ROOT" ;;
        *) echo "拒绝清理范围外的程序测试目录" >&2; return 1 ;;
    esac
}
trap cleanup EXIT
if [ "$(stat -c '%d' "$TEST_ROOT")" != "$(stat -c '%d' "$TEST_BIN_ROOT")" ]; then
    echo 'installer migration tests use separate filesystems'
fi
sed '/^main "$@"$/d' "$REPO_ROOT/install.sh" >"$TEST_ROOT/library.sh"

# 每个用例单独启动 Bash，保留安装器真实的 ERR/EXIT 处理和文件操作。
# 所有程序、配置、服务文件和管理入口都位于本次创建的隔离目录。
cat >"$TEST_ROOT/driver.sh" <<'DRIVER'
#!/usr/bin/env bash
set -Eeuo pipefail
CASE_NAME="$1"
CASE_ROOT="$2"
source "$3"
INSTALL_ROOT="$CASE_ROOT/etc"
BACKUP_DIR="$INSTALL_ROOT/backups"
INSTALL_META="$INSTALL_ROOT/install-meta.json"
CONFIG_FILE="$INSTALL_ROOT/config.yml"
CREDENTIALS_FILE="$INSTALL_ROOT/credentials.env"
BIN_DIR_FILE="$INSTALL_ROOT/bin-dir"
DEFAULT_BIN_DIR="$CASE_ROOT/original"
BIN_DIR="$CASE_ROOT/target"
CLI_SYMLINK_PATH="$CASE_ROOT/entry/xbctl"
SERVICE_PATH="$CASE_ROOT/xboard-node"
SERVICE_NAME=xboard-node
APP_NAME=xboard-node
INSTALLER_COPY_PATH="$INSTALL_ROOT/install.sh"
SERVICE_MANAGER=openrc
HEALTH_ENABLED=0
ARCH=amd64
YES=1
ACTION=upgrade
RELEASE_VERSION=v1.13-yz.23
RESTARTS=0
mkdir -p "$INSTALL_ROOT" "$DEFAULT_BIN_DIR" "$CASE_ROOT/entry" "$CASE_ROOT/package" "$BIN_DIR"
printf 'keep\n' >"$BIN_DIR/unrelated"
printf 'keep\n' >"$DEFAULT_BIN_DIR/unrelated"
cat >"$CASE_ROOT/package/xboard-node" <<'NODE'
#!/usr/bin/env bash
printf 'new-xboard-node\n'
NODE
cat >"$CASE_ROOT/package/xbctl" <<'CLI'
#!/usr/bin/env bash
set -euo pipefail
case "${1-} ${2-}" in
    'version ') printf 'new-xbctl\n' ;;
    'config bin-dir') printf '/custom-directory-supported\n' ;;
    'config health-port') printf '0\n' ;;
    'config refresh-meta')
        shift 2
        while [ $# -gt 0 ]; do
            case "$1" in --meta) meta="$2" ;; esac
            shift 2
        done
        printf '{"version":"new"}\n' >"$meta"
        ;;
    'config init')
        shift 2
        while [ $# -gt 0 ]; do
            case "$1" in
                --output) output="$2" ;;
                --credentials-out) credentials="$2" ;;
                --meta) meta="$2" ;;
            esac
            shift 2
        done
        printf 'health_port: 0\n' >"$output"
        printf 'TEST_INSTALLATION=1\n' >"$credentials"
        printf '{"version":"new"}\n' >"$meta"
        printf 'INSTANCE_ID=installation-fixture\nENV_KEY=TEST_INSTALLATION\n'
        ;;
    *) exit 1 ;;
esac
CLI
chmod 755 "$CASE_ROOT/package/"*
BINARY_SOURCE="$CASE_ROOT/package/xboard-node"
CLI_BINARY_SOURCE="$CASE_ROOT/package/xbctl"

if [ "$CASE_NAME" = fresh ]; then
    cp "$CLI_BINARY_SOURCE" "$BINARY_SOURCE"
    sed -i '/^case /i if [ "${1-}" = -v ]; then printf "yz-agent v1.14.0\\n"; exit 0; fi' "$BINARY_SOURCE"
    SERVICE_NAME=yz-agent
    APP_NAME=yz-agent
    SERVICE_PATH="$CASE_ROOT/yz-agent"
fi

if [ "$CASE_NAME" != fresh ]; then
    printf '#!/usr/bin/env bash\nprintf old-xboard-node\\n\n' >"$DEFAULT_BIN_DIR/xboard-node"
    cp "$CLI_BINARY_SOURCE" "$DEFAULT_BIN_DIR/xbctl"
    printf '\n# original-xbctl\n' >>"$DEFAULT_BIN_DIR/xbctl"
    chmod 755 "$DEFAULT_BIN_DIR/"*
    cp "$DEFAULT_BIN_DIR/xboard-node" "$CASE_ROOT/original-node-fixture"
    cp "$DEFAULT_BIN_DIR/xbctl" "$CASE_ROOT/original-cli-fixture"
    ln -s "$DEFAULT_BIN_DIR/xbctl" "$CLI_SYMLINK_PATH"
    printf 'health_port: 0\n' >"$CONFIG_FILE"
    printf 'TEST_INSTALLATION=1\n' >"$CREDENTIALS_FILE"
    printf '{"version":"original"}\n' >"$INSTALL_META"
    printf 'original-service\n' >"$SERVICE_PATH"
fi

case "$CASE_NAME" in
    fresh)
        ACTION=install
        PANEL_URL=https://panel.example.invalid
        TOKEN=installation-fixture
        NODE_ID=1
        MODE=node
        ;;
    repeat|uninstall)
        mv "$DEFAULT_BIN_DIR/xboard-node" "$BIN_DIR/xboard-node"
        mv "$DEFAULT_BIN_DIR/xbctl" "$BIN_DIR/xbctl"
        printf '%s\n' "$BIN_DIR" >"$BIN_DIR_FILE"
        ln -sfn "$BIN_DIR/xbctl" "$CLI_SYMLINK_PATH"
        BIN_DIR=""
        [ "$CASE_NAME" != uninstall ] || ACTION=uninstall
        ;;
    replace-failure|restart-failure-inplace|backup-failure|interrupted-inplace|interrupted-node-replace|interrupted-cli-replace) BIN_DIR="" ;;
    conflict) printf 'unrelated-program\n' >"$BIN_DIR/xboard-node" ;;
    invalid-path) BIN_DIR=relative/path ;;
    invalid-record) printf '%s\n\n' "$DEFAULT_BIN_DIR" >"$BIN_DIR_FILE" ;;
    entry-in-old-dir|entry-in-old-dir-failure)
        rm "$CLI_SYMLINK_PATH"
        CLI_SYMLINK_PATH="$DEFAULT_BIN_DIR/xbctl"
        ;;
    entry-in-new-dir|entry-in-new-dir-failure) BIN_DIR="$CASE_ROOT/entry" ;;
esac

service_stop() { :; }
service_start() { :; }
service_disable() { :; }
service_enable() { :; }
service_reload() { :; }
service_reset_failed() { :; }
service_is_active() { [ -f "$SERVICE_PATH" ]; }
wait_for_health() { :; }
show_recent_logs() { :; }
service_restart() {
    RESTARTS=$((RESTARTS + 1))
    case "$CASE_NAME" in
        interrupted-inplace|interrupted-migration)
            if [ "$RESTARTS" -eq 1 ]; then kill -TERM "$$"; fi
            ;;
        restart-failure-inplace|restart-failure-migration|entry-in-old-dir-failure|entry-in-new-dir-failure)
            if [ "$RESTARTS" -eq 1 ]; then return 1; fi
            ;;
    esac
}
if [ "$CASE_NAME" = replace-failure ]; then
    mv() {
        if [ "${2-}" = "$BIN_STAGE_DIR/xbctl" ]; then return 1; fi
        command mv "$@"
    }
fi
if [ "$CASE_NAME" = backup-failure ]; then
    cp() {
        if [ "${1-}" = "$CONFIG_FILE" ]; then return 1; fi
        command cp "$@"
    }
fi
if [ "$CASE_NAME" = interrupted-node-replace ] || [ "$CASE_NAME" = interrupted-cli-replace ]; then
    mv() {
        command mv "$@"
        if { [ "$CASE_NAME" = interrupted-node-replace ] && [ "${2-}" = "$BIN_STAGE_DIR/xboard-node" ]; } ||
           { [ "$CASE_NAME" = interrupted-cli-replace ] && [ "${2-}" = "$BIN_STAGE_DIR/xbctl" ]; }; then
            kill -TERM "$$"
        fi
    }
fi
if [ "$CASE_NAME" = download-failure ]; then
    stage_xbctl() { printf partial >"$BIN_STAGE_DIR/xbctl"; return 1; }
fi
lock_installation
load_install_paths
case "$ACTION" in
    install) perform_install ;;
    upgrade) perform_upgrade ;;
    uninstall) perform_uninstall ;;
esac
DRIVER

require_equal() {
    if [ "$1" != "$2" ]; then
        printf '断言失败：%s，实际=%s，预期=%s\n' "$3" "$1" "$2" >&2
        exit 1
    fi
}

for case_name in fresh migrate repeat uninstall replace-failure restart-failure-inplace restart-failure-migration download-failure backup-failure conflict invalid-path invalid-record entry-in-old-dir entry-in-old-dir-failure entry-in-new-dir entry-in-new-dir-failure interrupted-inplace interrupted-migration interrupted-node-replace interrupted-cli-replace; do
    case_root="$TEST_ROOT/$case_name"
    mkdir -p "$case_root" "$TEST_BIN_ROOT/$case_name"
    ln -s "$TEST_BIN_ROOT/$case_name" "$case_root/target"
    expected=0
    case "$case_name" in *failure*|conflict|invalid-*) expected=1 ;; esac
    case "$case_name" in interrupted-*) expected=143 ;; esac
    actual=0
    bash "$TEST_ROOT/driver.sh" "$case_name" "$case_root" "$TEST_ROOT/library.sh" >"$TEST_ROOT/$case_name.log" 2>&1 || actual=$?
    if [ "$actual" -ne "$expected" ]; then
        cat "$TEST_ROOT/$case_name.log" >&2
        require_equal "$actual" "$expected" "$case_name 退出状态"
    fi
    if find "$case_root" "$TEST_BIN_ROOT/$case_name" -name '.yz-agent-install.*' -o -name '.yz-agent-entry.*' -o -name '.install-lock' | grep .; then
        echo "$case_name 未清理本次临时文件或锁" >&2
        exit 1
    fi
    require_equal "$(cat "$case_root/target/unrelated")" keep "$case_name 保留无关文件"
    case "$case_name" in
        fresh|migrate|repeat|entry-in-old-dir|entry-in-new-dir)
            installed=$(cd "$case_root/target" && pwd -P)
            [ "$case_name" != entry-in-new-dir ] || installed="$case_root/entry"
            node_name=xboard-node
            [ "$case_name" != fresh ] || node_name=yz-agent
            cmp "$installed/$node_name" "$case_root/package/xboard-node"
            if [ "$case_name" = fresh ]; then
                [ ! -e "$installed/xbctl" ]
            else
                cmp "$installed/xbctl" "$case_root/package/xbctl"
            fi
            require_equal "$(cat "$case_root/etc/bin-dir")" "$installed" "$case_name 保存目录"
            grep -F "command=\"$installed/$node_name\"" "$case_root/$node_name" >/dev/null
            grep -F 'need net localmount' "$case_root/$node_name" >/dev/null
            if [ "$case_name" != entry-in-new-dir ]; then
                cli_name=xbctl
                [ "$case_name" != fresh ] || cli_name=yz-agent
                entry="$case_root/entry/$cli_name"
                [ "$case_name" != entry-in-old-dir ] || entry="$case_root/original/xbctl"
                require_equal "$(readlink "$entry")" "$installed/$cli_name" "$case_name 管理入口"
            fi
            [ ! -f "$case_root/original/xboard-node" ]
            ;;
        uninstall)
            [ ! -e "$case_root/target/xboard-node" ]
            [ ! -e "$case_root/target/xbctl" ]
            [ ! -e "$case_root/etc/bin-dir" ]
            [ -f "$case_root/etc/config.yml" ]
            ;;
        *)
            cmp "$case_root/original/xboard-node" "$case_root/original-node-fixture"
            cmp "$case_root/original/xbctl" "$case_root/original-cli-fixture"
            require_equal "$(cat "$case_root/xboard-node")" original-service "$case_name 恢复服务"
            require_equal "$(cat "$case_root/etc/install-meta.json")" '{"version":"original"}' "$case_name 恢复元数据"
            if [ "$case_name" = conflict ]; then
                require_equal "$(cat "$case_root/target/xboard-node")" unrelated-program '目标冲突不能覆盖文件'
            fi
            if [ "$case_name" != invalid-record ]; then [ ! -e "$case_root/etc/bin-dir" ]; fi
            if [ "$case_name" != entry-in-old-dir-failure ]; then
                require_equal "$(readlink "$case_root/entry/xbctl")" "$case_root/original/xbctl" "$case_name 恢复入口"
            fi
            ;;
    esac
done

echo 'installer custom-directory tests passed (20 scenarios)'
