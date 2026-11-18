#!/usr/bin/env bash
set -Eeuo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# 管道、日志文件和明确禁用颜色的终端使用纯文本。
if [ ! -t 1 ] || [ "${TERM:-dumb}" = dumb ] || [ -n "${NO_COLOR+x}" ]; then
    RED='' GREEN='' YELLOW='' CYAN='' BOLD='' NC=''
fi

APP_NAME="yz-agent"
CURRENT_INSTALL_ROOT="/etc/yz-agent"
LEGACY_INSTALL_ROOT="/etc/xboard-node"
INSTALL_ROOT="$CURRENT_INSTALL_ROOT"
PREVIOUS_INSTALL_ROOT=""
TARGET_INSTALL_ROOT=""
ROOT_MIGRATION_STARTED=0
ROOT_SOURCE_ID=""
BACKUP_DIR="${INSTALL_ROOT}/backups"
INSTALL_META="${INSTALL_ROOT}/install-meta.json"
CONFIG_FILE="${INSTALL_ROOT}/config.yml"
CREDENTIALS_FILE="${INSTALL_ROOT}/credentials.env"
DEFAULT_BIN_DIR="/usr/local/bin"
BIN_DIR=""
BIN_DIR_FILE="${INSTALL_ROOT}/bin-dir"
PREVIOUS_BIN_DIR=""
PREVIOUS_BINARY_PATH=""
PREVIOUS_CLI_PATH=""
PREVIOUS_CLI_ENTRY_PATH=""
BINARY_PATH="/usr/local/bin/yz-agent"
SERVICE_NAME="yz-agent"
SYSTEMD_SERVICE_NAME="${SERVICE_NAME}.service"
SYSTEMD_SERVICE_PATH="/etc/systemd/system/${SYSTEMD_SERVICE_NAME}"
OPENRC_SERVICE_PATH="/etc/init.d/${SERVICE_NAME}"
OPENRC_LOG_PATH="/var/log/${SERVICE_NAME}.log"
SERVICE_MANAGER=""
SERVICE_PATH=""
CLI_NAME="yz-agent"
CLI_PATH="/usr/local/bin/yz-agent"
CLI_SYMLINK_PATH="/usr/bin/yz-agent"
RUN_COMMAND="run "
INSTALLER_COPY_PATH="${INSTALL_ROOT}/install.sh"
CLI_BINARY_SOURCE=""
FIREWALL_CLEANUP_PROGRAM=""
DEFAULT_HEALTH_PORT=65530
DEFAULT_KERNEL="singbox"
DEFAULT_MODE="node"
DEFAULT_ACTION="install"
DEFAULT_RELEASE_VERSION="latest"
DEFAULT_LOG_LEVEL="info"
DEFAULT_KERNEL_LOG_LEVEL="warn"
DEFAULT_DOWNLOAD_BASE="https://github.com/P0me1oo/YZ-Agent/releases"
RELEASE_METADATA_URL="https://api.github.com/repos/P0me1oo/YZ-Agent/releases/latest"
RELEASE_MIRRORS=("https://gh-proxy.org/" "https://ghfast.top/" "https://github.chenc.dev/")
GITHUB_TIMED_OUT=0
CHECKSUMS_UNAVAILABLE=0
MIRROR_PROBE_DIR=""
RANKED_MIRRORS=()
RANKED_MIRROR_TIMES=()
RANKED_MIRROR_FILES=()
LATEST_RELEASE_URL=""

ACTION="${DEFAULT_ACTION}"
MODE=""
PANEL_URL=""
TOKEN=""
NODE_ID=""
NODE_TYPE=""
MACHINE_ID=""
KERNEL_TYPE="${DEFAULT_KERNEL}"
KERNEL_EXPLICIT=0
RELEASE_VERSION="${DEFAULT_RELEASE_VERSION}"
HEALTH_PORT="${DEFAULT_HEALTH_PORT}"
HEALTH_ENABLED=1
HEALTH_WAIT_SECONDS=180
RUNTIME_GOMEMLIMIT=""
RUNTIME_GOGC=""
BINARY_SOURCE=""
CLI_BINARY_SOURCE=""
FORCE_RECONFIGURE=0
PURGE=0
YES=0
ARCH=""
OS=""
DOWNLOAD_URL=""
CURRENT_STATE="fresh"
TMP_DIR=""
BIN_STAGE_DIR=""
BIN_DIR_RECORD_TMP=""
CLI_ENTRY_BACKUP_DIR=""
CLI_ENTRY_CHANGED=0
BINARIES_CHANGED=0
BINARY_REPLACE_STARTED=0
PRIVATE_LEGACY_EXPORT=0
CLI_REPLACE_STARTED=0
ROLLBACK_FAILED=0
LOCK_HELD=0
INSTALL_COMMITTED=0
BACKUP_PATH=""
BACKUP_PENDING=""
SERVICE_EXISTED=0
REPLACEMENT_SERVICE_STARTED=0
PREVIOUS_SERVICE_NAME=""
PREVIOUS_SERVICE_PATH=""
NAME_CHANGED=0
OLD_SERVICE_DISABLED=0
OLD_SERVICE_ENABLED=0
CLEANUP_DONE=0
INSTALLED_VERSION=""

log_info()   { printf '%b[信息]%b %s\n' "$GREEN" "$NC" "$1"; }
log_warn()   { printf '%b[警告]%b %s\n' "$YELLOW" "$NC" "$1"; }
log_error()  { printf '%b[错误]%b %s\n' "$RED" "$NC" "$1"; }
log_step()   { printf '%b[步骤]%b %b%s%b\n' "$CYAN" "$NC" "$BOLD" "$1" "$NC"; }
log_config() { printf '%b[配置]%b %s\n' "$CYAN" "$NC" "$1"; }

show_banner() {
    printf '\n===========================================\n'
    printf '%b    %s%b\n' "$BOLD" "$1" "$NC"
    printf '===========================================\n'
}

show_install_config() {
    local action mode version
    case "$ACTION" in install) action=安装 ;; upgrade) action=升级 ;; uninstall) action=卸载 ;; esac
    version="$RELEASE_VERSION"
    [ "$version" != latest ] || version='最新正式版（latest）'
    show_banner "YZ-Agent ${action}程序"
    log_config "操作：${action}"
    log_config "系统：${OS} / ${ARCH}"
    log_config "服务：${SERVICE_NAME}（${SERVICE_MANAGER}）"
    log_config "数据目录：${INSTALL_ROOT}"
    log_config "程序目录：${BIN_DIR}"
    if [ "$ACTION" = uninstall ]; then return; fi
    log_config "目标版本：${version}"
    if [ "$ACTION" = install ]; then
        case "$MODE" in node) mode="节点模式（节点 ${NODE_ID}）" ;; machine) mode="机器模式（机器 ${MACHINE_ID}）" ;; esac
        log_config "部署模式：${mode}"
        log_config "认证令牌：***"
        if [ "$KERNEL_EXPLICIT" -eq 1 ]; then
            log_config "核心：${KERNEL_TYPE}"
        else
            log_config "核心：根据面板配置和已有实例选择"
        fi
    fi
}

show_completion() {
    show_banner "YZ-Agent $1完成"
    log_config "版本：${INSTALLED_VERSION}"
    log_config "服务：${SERVICE_NAME}（${SERVICE_MANAGER}，已运行）"
    log_config "程序：${BINARY_PATH}"
    log_config "配置：${CONFIG_FILE}"
    if [ "$HEALTH_ENABLED" -eq 1 ]; then
        log_config "健康检查：http://127.0.0.1:${HEALTH_PORT}/healthz（已通过）"
    else
        log_config "健康检查：已禁用，仅确认服务运行"
    fi
    log_config "管理命令：${CLI_PATH}"
}

cleanup_tmp() {
    if [ "$CLEANUP_DONE" -eq 1 ]; then
        return
    fi
    CLEANUP_DONE=1
    cleanup_mirror_probe
    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
    [ -z "$BIN_DIR_RECORD_TMP" ] || rm -f "$BIN_DIR_RECORD_TMP"
    [ -z "$BACKUP_PENDING" ] || rm -rf "$BACKUP_PENDING"
    if [ "$ROLLBACK_FAILED" -eq 0 ]; then
        [ -z "$BIN_STAGE_DIR" ] || rm -rf "$BIN_STAGE_DIR"
        [ -z "$CLI_ENTRY_BACKUP_DIR" ] || rm -rf "$CLI_ENTRY_BACKUP_DIR"
    else
        log_error "已保留恢复文件：${BACKUP_PATH} ${BIN_STAGE_DIR} ${CLI_ENTRY_BACKUP_DIR}"
    fi
    if [ "$LOCK_HELD" -eq 1 ]; then
        rmdir "$INSTALL_ROOT/.install-lock" || true
    fi
}

load_health_port_from_config() {
    local cfg_path="$1"
    if [ ! -f "$cfg_path" ]; then
        return
    fi
    local parsed
    local reader="${2:-${PREVIOUS_CLI_PATH:-$CLI_PATH}}"
    if [ -x "$reader" ]; then
        parsed=$("$reader" config health-port --config "$cfg_path" 2>/dev/null)
    else
        parsed=$(grep -m1 'health_port:' "$cfg_path" 2>/dev/null | sed 's/.*health_port:[[:space:]]*//' | tr -cd '0-9')
    fi
    if [ -n "$parsed" ] && [ "$parsed" -ge 0 ] 2>/dev/null; then
        HEALTH_PORT="$parsed"
        if [ "$HEALTH_PORT" -eq 0 ]; then
            HEALTH_ENABLED=0
        else
            HEALTH_ENABLED=1
        fi
    fi
}

same_directory_entry() {
    local first second
    first=$(stat -c '%d:%i' "$1") || return 1
    second=$(stat -c '%d:%i' "$2") || return 1
    [ "$first" = "$second" ]
}

rollback_install() {
    log_warn "正在恢复原安装"
    if [ -f "$SERVICE_PATH" ] || service_is_active; then
        if ! service_stop >/dev/null 2>&1; then
            ROLLBACK_FAILED=1
            log_error "无法停止新服务，已保留恢复文件"
            return 1
        fi
    fi
    if [ "$REPLACEMENT_SERVICE_STARTED" -eq 1 ]; then
        if ! cleanup_installation_firewall; then
            ROLLBACK_FAILED=1
            log_error "无法清理新安装的防火墙规则，已保留恢复文件"
            return 1
        fi
    fi
    local failed=0 name target previous
    # 新版已写入加密状态时，恢复不支持该格式的旧程序前先安全解密。
    previous="$BIN_STAGE_DIR/previous-$APP_NAME"
    if [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ]; then previous="$PREVIOUS_BINARY_PATH"; fi
    if [ "$BINARY_REPLACE_STARTED" -eq 1 ] && [ -x "$BINARY_PATH" ] &&
        "$BINARY_PATH" config init --supports-private-storage >/dev/null 2>&1 &&
        { [ ! -x "$previous" ] || ! "$previous" config init --supports-private-storage >/dev/null 2>&1; }; then
        local export_args=(config private-export --config "$CONFIG_FILE")
        if [ -n "$BACKUP_PATH" ] && [ -d "$BACKUP_PATH" ]; then export_args+=(--backup "$BACKUP_PATH"); fi
        if ! "$BINARY_PATH" "${export_args[@]}"; then
            ROLLBACK_FAILED=1
            log_error "无法为旧版本恢复加密数据，已保留当前程序和恢复文件"
            service_start >/dev/null 2>&1 || true
            return 1
        fi
    fi
    if [ "$BINARIES_CHANGED" -eq 1 ]; then
        local names=("$APP_NAME")
        if [ "$CLI_NAME" != "$APP_NAME" ]; then names+=("$CLI_NAME"); fi
        for name in "${names[@]}"; do
            if [ "$name" = "$APP_NAME" ] && [ "$BINARY_REPLACE_STARTED" -eq 0 ]; then continue; fi
            if [ "$name" = xbctl ] && [ "$CLI_REPLACE_STARTED" -eq 0 ]; then continue; fi
            target="$BIN_DIR/$name"
            previous="$BIN_STAGE_DIR/previous-$name"
            if [ "$BIN_DIR" = "$PREVIOUS_BIN_DIR" ] && { [ -e "$previous" ] || [ -L "$previous" ]; }; then
                if ! same_directory_entry "$previous" "$target" 2>/dev/null; then
                    mv -f "$previous" "$target" || failed=1
                fi
            else
                rm -f "$target" || failed=1
            fi
        done
    fi
    if [ "$CLI_ENTRY_CHANGED" -eq 1 ]; then
        if [ -n "$CLI_ENTRY_BACKUP_DIR" ] && { [ -e "$CLI_ENTRY_BACKUP_DIR/xbctl" ] || [ -L "$CLI_ENTRY_BACKUP_DIR/xbctl" ]; }; then
            if ! same_directory_entry "$CLI_ENTRY_BACKUP_DIR/xbctl" "$CLI_SYMLINK_PATH" 2>/dev/null; then
                mv -f "$CLI_ENTRY_BACKUP_DIR/xbctl" "$CLI_SYMLINK_PATH" || failed=1
            fi
        else
            rm -f "$CLI_SYMLINK_PATH" || failed=1
        fi
    fi
    if [ -n "$BACKUP_PATH" ] && [ -d "$BACKUP_PATH" ]; then
        local item mode
        for item in config.yml credentials.env install-meta.json bin-dir install.sh service; do
            case "$item" in
                config.yml) target="$CONFIG_FILE"; mode=600 ;;
                credentials.env) target="$CREDENTIALS_FILE"; mode=600 ;;
                install-meta.json) target="$INSTALL_META"; mode=600 ;;
                bin-dir) target="$BIN_DIR_FILE"; mode=644 ;;
                install.sh) target="$INSTALLER_COPY_PATH"; mode=755 ;;
                service) target="${PREVIOUS_SERVICE_PATH:-$SERVICE_PATH}"; mode=$(service_file_mode) ;;
            esac
            if [ -f "$BACKUP_PATH/$item" ]; then
                install -m "$mode" "$BACKUP_PATH/$item" "$target" || failed=1
            else
                rm -f "$target" || failed=1
            fi
        done
    fi
    restore_install_root || failed=1
    if [ "$NAME_CHANGED" -eq 1 ]; then
        service_disable >/dev/null 2>&1 || true
        rm -f "$SERVICE_PATH" || failed=1
        set_service_name "$PREVIOUS_SERVICE_NAME"
        if [ "$OLD_SERVICE_DISABLED" -eq 1 ] && [ "$OLD_SERVICE_ENABLED" -eq 1 ]; then service_enable || failed=1; fi
    fi
    if [ "$failed" -ne 0 ]; then
        ROLLBACK_FAILED=1
        log_error "恢复未完成，将保留恢复文件"
        return 1
    fi
    CLI_PATH="$PREVIOUS_CLI_PATH"
    load_health_port_from_config "$CONFIG_FILE"
    service_reload || true
    if [ "$SERVICE_EXISTED" -eq 1 ] || [ -f "$SERVICE_PATH" ]; then
        service_reset_failed >/dev/null 2>&1 || true
        service_restart >/dev/null 2>&1 || true
        if ! wait_for_health; then
            log_error "原文件已恢复，但服务未通过健康检查"
            show_recent_logs
            return 1
        fi
    else
        service_disable >/dev/null 2>&1 || true
    fi
    log_warn "原安装已恢复"
}

on_error() {
    local exit_code=$?
    local line_no=${1:-未知}
    trap - ERR
    trap '' INT TERM
    if [ "$exit_code" -ne 0 ]; then
        log_error "操作失败：脚本第 ${line_no} 行，退出码 ${exit_code}"
        if [ -n "$BACKUP_PATH" ] && [ "$INSTALL_COMMITTED" -eq 0 ]; then
            rollback_install || true
        fi
    fi
    cleanup_tmp
    exit "$exit_code"
}

on_signal() {
    local exit_code="$1"
    # Linux Bash 的信号退出可能继续触发 ERR，必须阻止恢复流程再次执行。
    trap - ERR
    trap '' INT TERM
    if [ -n "$BACKUP_PATH" ] && [ "$INSTALL_COMMITTED" -eq 0 ]; then
        rollback_install || true
    fi
    cleanup_tmp
    exit "$exit_code"
}
trap 'on_error $LINENO' ERR
trap cleanup_tmp EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

usage() {
    cat <<'HELP'

  YZ-Agent 安装程序

  操作：
    install      安装或合并已有部署配置（默认）
    upgrade      升级程序并重启服务
    uninstall    卸载服务和程序；默认保留配置，--purge 同时删除数据
    status       查看当前安装状态
    help         显示帮助

  部署模式（省略时根据节点或机器编号自动选择）：
    --mode node      面板单节点模式（默认）
    --mode machine   面板机器模式

  节点模式必填：
    --panel, -a      面板地址
    --token, -t      节点认证令牌
    --node-id, -n    节点编号

  机器模式必填：
    --panel, -a       面板地址
    --token, -t       机器认证令牌
    --machine-id      机器编号

  可选参数：
    --node-type, -T     指定节点协议类型
    --kernel, -k        xray 或 singbox；新建默认 singbox，VLESS 默认 xray，已有实例保留原内核
    --version           正式发布版本或 latest（默认：latest）
    --binary            显式指定本地节点程序路径
    --xbctl-binary      为历史版本指定本地管理程序
    --bin-dir           程序目录，默认 /usr/local/bin；升级时可迁移已有安装
    --health-port       本机健康检查端口（默认：65530，0 表示禁用）
    --gomemlimit        Go 运行时内存限制，例如 256MiB
    --gogc              Go 垃圾回收目标百分比，例如 50
    --force-reconfigure 历史兼容参数；安装默认合并已有绑定
    --purge             卸载时同时删除安装数据目录
    --yes, -y           跳过卸载确认

  示例：
    sudo bash install.sh --panel https://panel.example.com --token TOKEN --node-id 1
    sudo bash install.sh --panel https://panel.example.com --token TOKEN --machine-id 1
    sudo bash install.sh upgrade
    sudo bash install.sh upgrade --bin-dir /boot/yz-agent
    sudo bash install.sh uninstall --purge --yes

HELP
}

require_option_value() {
    if [ $# -lt 2 ] || [ -z "$2" ] || [[ "$2" == --* || "$2" == -[atnTkhy] ]]; then
        log_error "参数 $1 缺少值；请使用 --help 查看用法" >&2
        return 1
    fi
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --mode|--panel|-a|--api|--token|-t|--node-id|-n|--node-type|-T|--machine-id|--kernel|-k|--version|--binary|--xbctl-binary|--bin-dir|--health-port|--gomemlimit|--gogc)
                require_option_value "$@" || return 1
                ;;
        esac
        case "$1" in
            install|upgrade|uninstall|status|help)
                ACTION="$1"
                shift
                ;;
            --mode)
                MODE="$2"
                shift 2
                ;;
            --panel|-a|--api)
                PANEL_URL="$2"
                shift 2
                ;;
            --token|-t)
                TOKEN="$2"
                shift 2
                ;;
            --node-id|-n)
                NODE_ID="$2"
                shift 2
                ;;
            --node-type|-T)
                NODE_TYPE="$2"
                shift 2
                ;;
            --machine-id)
                MACHINE_ID="$2"
                shift 2
                ;;
            --kernel|-k)
                KERNEL_TYPE="$2"
                KERNEL_EXPLICIT=1
                shift 2
                ;;
            --version)
                if [ "$2" != latest ] && ! upgrade_version_key "$2" >/dev/null; then
                    log_error "目标版本格式无效，请指定 latest、正式版本或受支持的历史版本" >&2
                    return 1
                fi
                RELEASE_VERSION="$2"
                shift 2
                ;;
            --binary)
                BINARY_SOURCE="$2"
                shift 2
                ;;
            --xbctl-binary)
                CLI_BINARY_SOURCE="$2"
                shift 2
                ;;
            --bin-dir)
                if [ $# -lt 2 ] || [ -z "$2" ]; then
                    log_error "--bin-dir 需要指定绝对路径" >&2
                    return 1
                fi
                BIN_DIR="$2"
                shift 2
                ;;
            --health-port)
                HEALTH_PORT="$2"
                shift 2
                ;;
            --gomemlimit)
                RUNTIME_GOMEMLIMIT="$2"
                shift 2
                ;;
            --gogc)
                RUNTIME_GOGC="$2"
                shift 2
                ;;
            --force-reconfigure)
                FORCE_RECONFIGURE=1
                shift
                ;;
            --purge)
                PURGE=1
                shift
                ;;
            --yes|-y)
                YES=1
                shift
                ;;
            --help|-h)
                ACTION="help"
                shift
                ;;
            *)
                log_error "存在未知参数或操作；请使用 --help 查看用法" >&2
                return 1
                ;;
        esac
    done

    case "$KERNEL_TYPE" in
        singbox|SingBox|SINGBOX) KERNEL_TYPE="singbox" ;;
        xray|Xray|XRAY) KERNEL_TYPE="xray" ;;
        *) ;;
    esac

    # 新建节点默认使用 sing-box，VLESS 仍默认使用 Xray；显式选择优先。
    if [ "$KERNEL_EXPLICIT" -eq 0 ] && [ -n "$NODE_TYPE" ]; then
        case "$(printf '%s' "$NODE_TYPE" | tr '[:upper:]' '[:lower:]')" in
            vless)
                KERNEL_TYPE="xray"
                ;;
        esac
    fi

    # Auto-detect mode from arguments when --mode is not specified.
    if [ -z "$MODE" ]; then
        if [ -n "$MACHINE_ID" ]; then
            MODE="machine"
        else
            MODE="node"
        fi
    fi

    case "$MODE" in
        node|machine) ;;
        *)
            log_error "不支持的部署模式：$MODE"
            usage
            exit 1
            ;;
    esac
}

check_root() {
    if [ "$(id -u)" -ne 0 ]; then
        log_error "请以 root 用户运行，或使用 sudo"
        exit 1
    fi
}

detect_arch() {
    local raw
    raw=$(uname -m)
    case "$raw" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *)
            log_error "不支持的处理器架构：$raw"
            exit 1
            ;;
    esac
}

detect_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        OS="$ID"
    else
        OS="unknown"
    fi
}

detect_service_manager() {
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        SERVICE_MANAGER="systemd"
        SERVICE_PATH="$SYSTEMD_SERVICE_PATH"
        return
    fi
    if command -v rc-service >/dev/null 2>&1 && \
       command -v rc-update >/dev/null 2>&1 && \
       command -v supervise-daemon >/dev/null 2>&1 && \
       [ -x /sbin/openrc-run ]; then
        SERVICE_MANAGER="openrc"
        SERVICE_PATH="$OPENRC_SERVICE_PATH"
        return
    fi
    log_error "需要正在运行的 systemd 或 OpenRC 服务管理器"
    exit 1
}

normalize_bin_dir() {
    local dir="$1"
    while [[ "$dir" == */ ]] && [ "$dir" != / ]; do dir="${dir%/}"; done
    if ! [[ "$dir" =~ ^/[A-Za-z0-9._/-]+$ ]] || [ "$dir" = / ] ||
       [[ "$dir/" == *'//'* || "$dir/" == *'/./'* || "$dir/" == *'/../'* ]]; then
        log_error "程序目录必须是绝对路径，仅允许字母、数字和 /._-，不能包含 . 或 .. 路径段" >&2
        return 1
    fi
    printf '%s\n' "$dir"
}

set_install_root() {
    local previous="$INSTALL_ROOT"
    INSTALL_ROOT="$1"
    BACKUP_DIR="$INSTALL_ROOT/backups"
    CONFIG_FILE="$INSTALL_ROOT/config.yml"
    CREDENTIALS_FILE="$INSTALL_ROOT/credentials.env"
    INSTALL_META="$INSTALL_ROOT/install-meta.json"
    BIN_DIR_FILE="$INSTALL_ROOT/bin-dir"
    INSTALLER_COPY_PATH="$INSTALL_ROOT/install.sh"
    if [[ "$BACKUP_PATH" == "$previous/"* ]]; then BACKUP_PATH="$INSTALL_ROOT/${BACKUP_PATH#"$previous/"}"; fi
}

select_install_root() {
    if { [ -e "$CURRENT_INSTALL_ROOT" ] || [ -L "$CURRENT_INSTALL_ROOT" ]; } &&
       { [ -e "$LEGACY_INSTALL_ROOT" ] || [ -L "$LEGACY_INSTALL_ROOT" ]; }; then
        log_error "新旧安装目录同时存在，请先解决目录冲突"
        return 1
    fi
    if [ -e "$LEGACY_INSTALL_ROOT" ] || [ -L "$LEGACY_INSTALL_ROOT" ]; then
        set_install_root "$LEGACY_INSTALL_ROOT"
    else
        set_install_root "$CURRENT_INSTALL_ROOT"
    fi
}

load_install_paths() {
    local installed_service="" name
    for name in yz-agent agent xboard-node; do
        if [ -e "$(dirname "$SYSTEMD_SERVICE_PATH")/$name.service" ] ||
           [ -e "$(dirname "$OPENRC_SERVICE_PATH")/$name" ]; then
            if [ -n "$installed_service" ]; then
                log_error "发现多个安装服务名称，请先解决服务冲突"
                return 1
            fi
            installed_service="$name"
        fi
    done
    local saved="$DEFAULT_BIN_DIR"
    if [ -f "$BIN_DIR_FILE" ]; then
        local saved_lines=()
        mapfile -t saved_lines <"$BIN_DIR_FILE"
        if [ "${#saved_lines[@]}" -ne 1 ]; then
            log_error "程序目录记录无效：$BIN_DIR_FILE" >&2
            return 1
        fi
        saved="${saved_lines[0]}"
    fi
    PREVIOUS_BIN_DIR=$(normalize_bin_dir "$saved")
    if [ -d "$PREVIOUS_BIN_DIR" ]; then
        PREVIOUS_BIN_DIR=$(cd "$PREVIOUS_BIN_DIR" && pwd -P)
        PREVIOUS_BIN_DIR=$(normalize_bin_dir "$PREVIOUS_BIN_DIR")
    fi
    BIN_DIR=$(normalize_bin_dir "${BIN_DIR:-$PREVIOUS_BIN_DIR}")
    if [ -d "$BIN_DIR" ]; then
        BIN_DIR=$(cd "$BIN_DIR" && pwd -P)
    fi
    if [ -n "$installed_service" ]; then
        set_service_name "$installed_service"
    else
        for name in yz-agent agent xboard-node; do
            if [ -e "$PREVIOUS_BIN_DIR/$name" ]; then set_service_name "$name"; break; fi
        done
    fi
    APP_NAME="$SERVICE_NAME"
    PREVIOUS_BINARY_PATH="$PREVIOUS_BIN_DIR/$APP_NAME"
    CLI_NAME="$APP_NAME"
    [ "$APP_NAME" != xboard-node ] || CLI_NAME=xbctl
    CLI_SYMLINK_PATH="$(dirname "$CLI_SYMLINK_PATH")/$CLI_NAME"
    PREVIOUS_CLI_ENTRY_PATH="$CLI_SYMLINK_PATH"
    PREVIOUS_CLI_PATH="$PREVIOUS_BIN_DIR/$CLI_NAME"
    BINARY_PATH="$BIN_DIR/$APP_NAME"
    CLI_PATH="$BIN_DIR/$CLI_NAME"
    if [ "$ACTION" != install ] && [ "$ACTION" != upgrade ] && [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ]; then
        log_error "修改程序目录（--bin-dir）需要使用 install 或 upgrade" >&2
        return 1
    fi
}

lock_installation() {
    mkdir -p "$INSTALL_ROOT"
    if [ "${YZ_INSTALL_PARENT_LOCK:-}" = 1 ] && [ -d "$INSTALL_ROOT/.install-lock" ]; then
        return
    fi
    if ! mkdir "$INSTALL_ROOT/.install-lock"; then
        log_error "可能有其他安装程序正在运行，重试前请检查 $INSTALL_ROOT/.install-lock" >&2
        return 1
    fi
    LOCK_HELD=1
}

# 程序与服务一起迁移，历史名称只用于发现旧安装和版本回退。
set_service_name() {
    SERVICE_NAME="$1"
    RUN_COMMAND="run "
    [ "$SERVICE_NAME" != xboard-node ] || RUN_COMMAND=""
    SYSTEMD_SERVICE_NAME="${SERVICE_NAME}.service"
    SYSTEMD_SERVICE_PATH="$(dirname "$SYSTEMD_SERVICE_PATH")/$SYSTEMD_SERVICE_NAME"
    OPENRC_SERVICE_PATH="$(dirname "$OPENRC_SERVICE_PATH")/$SERVICE_NAME"
    OPENRC_LOG_PATH="$(dirname "$OPENRC_LOG_PATH")/$SERVICE_NAME.log"
    if [ -n "$SERVICE_PATH" ]; then
        case "$SERVICE_MANAGER" in
            systemd) SERVICE_PATH="$(dirname "$SERVICE_PATH")/$SYSTEMD_SERVICE_NAME" ;;
            openrc) SERVICE_PATH="$(dirname "$SERVICE_PATH")/$SERVICE_NAME" ;;
        esac
    fi
}

select_target_name() {
    local report="$1" target=xboard-node
    case "$report" in 'yz-agent '*) target=yz-agent ;; 'agent '*) target=agent ;; esac
    PREVIOUS_SERVICE_NAME="$SERVICE_NAME"
    PREVIOUS_SERVICE_PATH="$SERVICE_PATH"
    if [ "$target" != "$SERVICE_NAME" ]; then
        set_service_name "$target"
        # 同名目标必须由当前安装拥有，不能覆盖其他程序或服务。
        if [ -e "$SERVICE_PATH" ] || [ -L "$SERVICE_PATH" ] ||
           [ -e "$BIN_DIR/$target" ] || [ -L "$BIN_DIR/$target" ]; then
            log_error "改名目标已存在：$SERVICE_PATH 或 $BIN_DIR/$target"
            return 1
        fi
        NAME_CHANGED=1
    fi
    APP_NAME="$target"
    BINARY_PATH="$BIN_DIR/$APP_NAME"
    CLI_NAME="$APP_NAME"
    [ "$APP_NAME" != xboard-node ] || CLI_NAME=xbctl
    CLI_PATH="$BIN_DIR/$CLI_NAME"
    CLI_SYMLINK_PATH="$(dirname "$CLI_SYMLINK_PATH")/$CLI_NAME"
    if [ "$CLI_SYMLINK_PATH" != "$PREVIOUS_CLI_ENTRY_PATH" ] &&
       [ "$CLI_SYMLINK_PATH" != "$CLI_PATH" ] &&
       { [ -e "$CLI_SYMLINK_PATH" ] || [ -L "$CLI_SYMLINK_PATH" ]; }; then
        log_error "命令入口已存在：$CLI_SYMLINK_PATH"
        return 1
    fi
    if [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ] && { [ -e "$BINARY_PATH" ] || [ -L "$BINARY_PATH" ]; }; then
        log_error "目标文件已存在：$BINARY_PATH"
        return 1
    fi
}

select_target_root() {
    if [ "$APP_NAME" = yz-agent ]; then
        FIREWALL_CLEANUP_PROGRAM="$BIN_STAGE_DIR/xboard-node"
    elif [ -x "$PREVIOUS_BINARY_PATH" ]; then
        case "$("$PREVIOUS_BINARY_PATH" -v)" in 'yz-agent '*) FIREWALL_CLEANUP_PROGRAM="$PREVIOUS_BINARY_PATH" ;; esac
    fi
    PREVIOUS_INSTALL_ROOT="$INSTALL_ROOT"
    TARGET_INSTALL_ROOT="$INSTALL_ROOT"
    # 测试和调用方指定的独立目录沿用原路径。
    if [ "$INSTALL_ROOT" != "$CURRENT_INSTALL_ROOT" ] && [ "$INSTALL_ROOT" != "$LEGACY_INSTALL_ROOT" ]; then return; fi
    TARGET_INSTALL_ROOT="$CURRENT_INSTALL_ROOT"
    [ "$APP_NAME" = yz-agent ] || TARGET_INSTALL_ROOT="$LEGACY_INSTALL_ROOT"
    if [ "$TARGET_INSTALL_ROOT" = "$INSTALL_ROOT" ]; then return; fi
    if [ -e "$TARGET_INSTALL_ROOT" ] || [ -L "$TARGET_INSTALL_ROOT" ]; then
        log_error "目标安装目录已存在：$TARGET_INSTALL_ROOT"
        return 1
    fi
    if [ -L "$INSTALL_ROOT" ] ||
       [ "$(stat -c %d "$INSTALL_ROOT")" != "$(stat -c %d "$(dirname "$TARGET_INSTALL_ROOT")")" ]; then
        log_error "安装目录迁移要求原目录不是符号链接，且位于同一文件系统"
        return 1
    fi
    local program_dir
    for program_dir in "$BIN_DIR" "$PREVIOUS_BIN_DIR"; do
        if [ "$program_dir" = "$INSTALL_ROOT" ] || [[ "$program_dir" == "$INSTALL_ROOT/"* ]]; then
            log_error "改名前请先将程序目录移到安装数据目录之外"
            return 1
        fi
    done
    ROOT_SOURCE_ID=$(stat -c '%d:%i' "$INSTALL_ROOT")
}

cleanup_installation_firewall() {
    [ -f "$CONFIG_FILE" ] || return 0
    local helper="$FIREWALL_CLEANUP_PROGRAM"
    if [ "$APP_NAME" = yz-agent ] && [ "$BINARY_REPLACE_STARTED" -eq 1 ] && [ -x "$BINARY_PATH" ]; then helper="$BINARY_PATH"; fi
    [ -n "$helper" ] || return 0
    "$helper" config cleanup-firewall --config "$CONFIG_FILE"
}

prepare_root_config() {
    [ "$TARGET_INSTALL_ROOT" != "$INSTALL_ROOT" ] || return 0
    local source="$CONFIG_FILE" helper="$PREVIOUS_BINARY_PATH"
    [ ! -f "$TMP_DIR/config.yml" ] || source="$TMP_DIR/config.yml"
    [ -f "$source" ] || return 0
    [ "$APP_NAME" != yz-agent ] || helper="$BIN_STAGE_DIR/xboard-node"
    "$helper" config migrate-root --config "$source" --output "$TMP_DIR/migrated-config.yml" \
        --from "$INSTALL_ROOT" --to "$TARGET_INSTALL_ROOT"
    chmod 600 "$TMP_DIR/migrated-config.yml"
    if [ "$source" = "$TMP_DIR/config.yml" ]; then
        mv -f "$TMP_DIR/migrated-config.yml" "$TMP_DIR/config.yml"
    fi
}

migrate_install_root() {
    [ "$TARGET_INSTALL_ROOT" != "$INSTALL_ROOT" ] || return 0
    ROOT_MIGRATION_STARTED=1
    mv -T "$INSTALL_ROOT" "$TARGET_INSTALL_ROOT"
    set_install_root "$TARGET_INSTALL_ROOT"
    # 父升级进程仍持有原路径的锁；事务期间旧路径指向同一把锁。
    ln -s "$TARGET_INSTALL_ROOT" "$PREVIOUS_INSTALL_ROOT"
    if [ -f "$TMP_DIR/migrated-config.yml" ]; then
        install -m 600 "$TMP_DIR/migrated-config.yml" "$CONFIG_FILE"
    fi
}

restore_install_root() {
    [ "$ROOT_MIGRATION_STARTED" -eq 1 ] || return 0
    if [ "$(stat -c '%d:%i' "$TARGET_INSTALL_ROOT" 2>/dev/null)" != "$ROOT_SOURCE_ID" ]; then
        [ "$(stat -c '%d:%i' "$PREVIOUS_INSTALL_ROOT" 2>/dev/null)" = "$ROOT_SOURCE_ID" ] || return 1
        ROOT_MIGRATION_STARTED=0
        # 错误回调中的无参数 return 在部分 Bash 版本会沿用原失败状态。
        return 0
    fi
    if [ -L "$PREVIOUS_INSTALL_ROOT" ] && [ "$(readlink "$PREVIOUS_INSTALL_ROOT")" = "$TARGET_INSTALL_ROOT" ]; then
        rm "$PREVIOUS_INSTALL_ROOT" || return 1
    elif [ -e "$PREVIOUS_INSTALL_ROOT" ] || [ -L "$PREVIOUS_INSTALL_ROOT" ]; then
        log_error "原安装路径已被占用，保留迁移后的目录用于恢复"
        return 1
    fi
    mv -T "$TARGET_INSTALL_ROOT" "$PREVIOUS_INSTALL_ROOT" || return 1
    set_install_root "$PREVIOUS_INSTALL_ROOT"
    ROOT_MIGRATION_STARTED=0
}

stop_previous_service() {
    if [ "$NAME_CHANGED" -eq 1 ] && [ -f "$PREVIOUS_SERVICE_PATH" ]; then
        case "$SERVICE_MANAGER" in
            systemd)
                if systemctl is-enabled "$PREVIOUS_SERVICE_NAME.service" >/dev/null 2>&1; then OLD_SERVICE_ENABLED=1; fi
                systemctl stop "$PREVIOUS_SERVICE_NAME.service"
                ;;
            openrc)
                if rc-update show default | awk -v name="$PREVIOUS_SERVICE_NAME" '$1 == name { found=1 } END { exit !found }'; then OLD_SERVICE_ENABLED=1; fi
                rc-service "$PREVIOUS_SERVICE_NAME" stop
                ;;
        esac
    fi
}

service_file_mode() {
    if [ "$SERVICE_MANAGER" = "openrc" ]; then
        echo 755
    else
        echo 644
    fi
}

service_reload() {
    if [ "$SERVICE_MANAGER" = "systemd" ]; then
        systemctl daemon-reload
    fi
}

service_reset_failed() {
    if [ "$SERVICE_MANAGER" = "systemd" ]; then
        systemctl reset-failed "$SYSTEMD_SERVICE_NAME"
    fi
}

service_is_active() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl is-active "$SYSTEMD_SERVICE_NAME" >/dev/null 2>&1 ;;
        openrc) rc-service --quiet "$SERVICE_NAME" status >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}

service_start() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl start "$SYSTEMD_SERVICE_NAME" ;;
        openrc) rc-service "$SERVICE_NAME" start ;;
    esac
}

service_stop() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl stop "$SYSTEMD_SERVICE_NAME" ;;
        openrc) rc-service "$SERVICE_NAME" stop ;;
    esac
}

service_restart() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl restart "$SYSTEMD_SERVICE_NAME" ;;
        openrc) rc-service "$SERVICE_NAME" restart ;;
    esac
}

service_enable() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl enable "$SYSTEMD_SERVICE_NAME" ;;
        openrc) rc-update add "$SERVICE_NAME" default ;;
    esac
}

service_disable() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl disable "$SYSTEMD_SERVICE_NAME" ;;
        openrc) rc-update del "$SERVICE_NAME" default ;;
    esac
}

service_status() {
    case "$SERVICE_MANAGER" in
        systemd) systemctl status "$SYSTEMD_SERVICE_NAME" --no-pager ;;
        openrc) rc-service "$SERVICE_NAME" status ;;
    esac
}

run_with_retry() {
    local attempts="$1"
    local delay="$2"
    shift 2
    local i=1
    while [ "$i" -le "$attempts" ]; do
        if "$@"; then
            return 0
        fi
        if [ "$i" -lt "$attempts" ]; then
            log_warn "命令执行失败，${delay} 秒后重试：$*"
            sleep "$delay"
        fi
        i=$((i + 1))
    done
    return 1
}

install_dependencies() {
    log_step "检查安装依赖"
    local packages=()
    mapfile -t packages < <(missing_dependency_packages)
    if [ "${#packages[@]}" -eq 0 ]; then
        log_info "依赖已满足，跳过安装"
        return 0
    fi
    log_info "安装缺少的依赖：${packages[*]}"
    case "$OS" in
        ubuntu|debian)
            DEBIAN_FRONTEND=noninteractive run_with_retry 10 3 apt-get update -qq || return $?
            DEBIAN_FRONTEND=noninteractive run_with_retry 10 3 apt-get install -y -qq "${packages[@]}" >/dev/null || return $?
            ;;
        centos|rhel|rocky|almalinux|fedora)
            if command -v dnf >/dev/null 2>&1; then
                run_with_retry 5 3 dnf install -y -q "${packages[@]}" >/dev/null || return $?
            else
                run_with_retry 5 3 yum install -y -q "${packages[@]}" >/dev/null || return $?
            fi
            ;;
        alpine)
            run_with_retry 5 3 apk add --no-cache "${packages[@]}" >/dev/null || return $?
            ;;
        *)
            log_error "系统 ${OS} 暂不支持自动安装依赖，请手动安装：${packages[*]}"
            return 1
            ;;
    esac
    mapfile -t packages < <(missing_dependency_packages)
    if [ "${#packages[@]}" -gt 0 ]; then
        log_error "安装后仍缺少依赖：${packages[*]}"
        return 1
    fi
    log_info "依赖安装完成"
}

has_ca_certificates() {
    local bundle
    for bundle in "${CURL_CA_BUNDLE:-}" "${SSL_CERT_FILE:-}" \
        /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt \
        /etc/ssl/ca-bundle.pem /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem; do
        if [ -n "$bundle" ] && [ -r "$bundle" ] && [ -s "$bundle" ]; then return 0; fi
    done
    return 1
}

missing_dependency_packages() {
    command -v curl >/dev/null 2>&1 || printf 'curl\n'
    has_ca_certificates || printf 'ca-certificates\n'
    local tool needs_coreutils=0
    for tool in stat mktemp install realpath sha256sum sort mv cp ln; do
        if ! command -v "$tool" >/dev/null 2>&1; then needs_coreutils=1; break; fi
    done
    # 目录迁移使用 mv -T，不能仅凭存在 BusyBox 同名命令就跳过 coreutils。
    if [ "$needs_coreutils" -eq 1 ] || ! mv --version >/dev/null 2>&1; then
        printf 'coreutils\n'
    fi
}

ensure_dirs() {
    mkdir -p "$INSTALL_ROOT" "$BACKUP_DIR" "$BIN_DIR"
    chmod 700 "$INSTALL_ROOT"
    BIN_DIR=$(cd "$BIN_DIR" && pwd -P)
    BIN_DIR=$(normalize_bin_dir "$BIN_DIR")
    BINARY_PATH="$BIN_DIR/$APP_NAME"
    CLI_PATH="$BIN_DIR/$CLI_NAME"
    local target
    for target in "$BINARY_PATH" "$CLI_PATH"; do
        if [ -e "$target" ] || [ -L "$target" ]; then
            if [ "$target" = "$BINARY_PATH" ] && [ "$APP_NAME" = yz-agent ] &&
               [ ! -f "$CONFIG_FILE" ] && [ ! -f "$INSTALL_META" ]; then
                log_error "已有 yz-agent 程序缺少安装记录：$target"
                return 1
            fi
            if [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ]; then
                # 迁移到 /usr/bin 时，只允许替换已经指向原 xbctl 的入口。
                if [ "$target" = "$CLI_SYMLINK_PATH" ] && [ -L "$target" ] &&
                   [ "$(readlink "$target")" = "$PREVIOUS_CLI_PATH" ]; then
                    continue
                fi
                log_error "目标文件已存在：$target"
                return 1
            fi
            if [ ! -f "$target" ] && [ ! -L "$target" ]; then
                log_error "程序路径不是普通文件或符号链接：$target"
                return 1
            fi
        fi
    done
    # 大文件直接暂存在目标分区；配置和凭据仍暂存在系统临时目录。
    BIN_STAGE_DIR=$(mktemp -d "$BIN_DIR/.yz-agent-install.XXXXXX")
}

validate_positive_int() {
    local label="$1"
    local value="$2"
    if ! [[ "$value" =~ ^[0-9]+$ ]] || [ "$value" -le 0 ]; then
        log_error "${label}必须是正整数"
        exit 1
    fi
}

validate_install_request() {
    if [ -z "$PANEL_URL" ]; then
        log_error "请提供面板地址（--panel）"
        exit 1
    fi
    if [ -z "$TOKEN" ]; then
        log_error "请提供认证令牌（--token）"
        exit 1
    fi
    if ! [[ "$HEALTH_PORT" =~ ^[0-9]+$ ]]; then
        log_error "健康检查端口（--health-port）必须是非负整数"
        exit 1
    fi
    if [ "$HEALTH_PORT" -eq 0 ]; then
        HEALTH_ENABLED=0
    fi
    case "$KERNEL_TYPE" in
        singbox|xray) ;;
        *)
            log_error "核心（--kernel）必须是 singbox 或 xray"
            exit 1
            ;;
    esac
    case "$MODE" in
        node)
            validate_positive_int "节点编号（--node-id）" "$NODE_ID"
            ;;
        machine)
            validate_positive_int "机器编号（--machine-id）" "$MACHINE_ID"
            ;;
    esac
}

detect_current_state() {
    local has_binary=0 has_config=0 has_service=0
    [ -x "${PREVIOUS_BINARY_PATH:-$BINARY_PATH}" ] && has_binary=1
    [ -f "$CONFIG_FILE" ] && has_config=1
    [ -f "$SERVICE_PATH" ] && has_service=1

    if [ "$has_binary" -eq 0 ] && [ "$has_config" -eq 0 ] && [ "$has_service" -eq 0 ]; then
        CURRENT_STATE="fresh"
    elif [ "$has_binary" -eq 1 ] && [ "$has_config" -eq 1 ] && [ "$has_service" -eq 1 ]; then
        CURRENT_STATE="installed"
    else
        CURRENT_STATE="partial"
    fi
}

require_reconfigure_confirmation() {
    return
}

cleanup_mirror_probe() {
    if [ -n "$MIRROR_PROBE_DIR" ] && [ -d "$MIRROR_PROBE_DIR" ]; then
        rm -rf "$MIRROR_PROBE_DIR"
    fi
    MIRROR_PROBE_DIR=""
}

mirror_release_tag() {
    local file="$1" tag draft prerelease
    # 只接受正式 Release 响应；返回值仅作为版本号使用，不执行响应内容。
    grep -q '^[[:space:]]*{' "$file" || return 1
    tag=$(sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"[:space:]]+)".*/\1/p' "$file")
    draft=$(sed -nE 's/.*"draft"[[:space:]]*:[[:space:]]*(true|false).*/\1/p' "$file")
    prerelease=$(sed -nE 's/.*"prerelease"[[:space:]]*:[[:space:]]*(true|false).*/\1/p' "$file")
    [ "$draft" = false ] && [ "$prerelease" = false ] || return 1
    upgrade_version_key "$tag" >/dev/null || return 1
    printf '%s' "$tag"
}

probe_release_mirrors() {
    local url="$1" kind="$2" index base elapsed file pid status code content_type
    local pids=()
    cleanup_mirror_probe
    MIRROR_PROBE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/yz-agent-mirrors.XXXXXX") || return 1
    RANKED_MIRRORS=() RANKED_MIRROR_TIMES=() RANKED_MIRROR_FILES=()
    for index in "${!RELEASE_MIRRORS[@]}"; do
        base="${RELEASE_MIRRORS[$index]}"
        (
            file="$MIRROR_PROBE_DIR/$index.body"
            # 清单缺失时直接探测程序的前 4 KiB；不因缺少清单而阻止程序下载。
            if [ "$kind" = artifact ]; then
                status=0
                elapsed=$(LC_ALL=C curl -q -fsSL --connect-timeout 3 --max-time 5 --range 0-4095 --max-filesize 4096 \
                    -o "$file" -w '%{time_total}\t%{http_code}\t%{content_type}' "${base}${url}" 2>/dev/null) || status=$?
                # 63 表示源忽略 Range 且文件超过探测上限，响应头仍可用于判定可达。
                [ "$status" -eq 0 ] || [ "$status" -eq 63 ] || exit 0
                IFS=$'\t' read -r elapsed code content_type <<< "$elapsed"
                [[ "$code" = 200 || "$code" = 206 ]] && [[ "$content_type" != text/html* ]] || exit 0
                if [ "$status" -eq 0 ]; then
                    [ -s "$file" ] || exit 0
                    if LC_ALL=C grep -aiEq '^[[:space:]]*<(![Dd][Oo][Cc][Tt][Yy][Pp][Ee]|[Hh][Tt][Mm][Ll])' "$file"; then exit 0; fi
                fi
                printf '%s\t%s\t%s\n' "$elapsed" "$base" "$file" > "$MIRROR_PROBE_DIR/$index.rank"
            elif elapsed=$(LC_ALL=C curl -q -fsSL --connect-timeout 3 --max-time 5 --max-filesize 1048576 \
                -o "$file" -w '%{time_total}' "${base}${url}" 2>/dev/null); then
                if [ "$kind" = latest ]; then
                    mirror_release_tag "$file" >/dev/null || exit 0
                else
                    grep -Eq '^[[:xdigit:]]{64}[[:blank:]]+\*?[^[:space:]]' "$file" || exit 0
                fi
                [[ "$elapsed" =~ ^[0-9]+([.][0-9]+)?$ ]] || exit 0
                printf '%s\t%s\t%s\n' "$elapsed" "$base" "$file" > "$MIRROR_PROBE_DIR/$index.rank"
            fi
            exit 0
        ) &
        pids+=("$!")
    done
    for pid in "${pids[@]}"; do wait "$pid" || true; done
    : > "$MIRROR_PROBE_DIR/results"
    for file in "$MIRROR_PROBE_DIR/"*.rank; do
        [ ! -f "$file" ] || cat "$file" >> "$MIRROR_PROBE_DIR/results"
    done
    while IFS=$'\t' read -r elapsed base file; do
        RANKED_MIRRORS+=("$base")
        RANKED_MIRROR_TIMES+=("$elapsed")
        RANKED_MIRROR_FILES+=("$file")
    done < <(LC_ALL=C sort -n "$MIRROR_PROBE_DIR/results")
    if [ "${#RANKED_MIRRORS[@]}" -eq 0 ]; then
        log_error "没有可用的 GitHub 镜像源" >&2
        cleanup_mirror_probe
        return 1
    fi
}

query_latest_release() {
    local status tag
    if [ "$GITHUB_TIMED_OUT" -eq 0 ]; then
        if LATEST_RELEASE_URL=$(curl -q -fsSL --connect-timeout 10 --max-time 30 \
            -o /dev/null -w '%{url_effective}' "${DEFAULT_DOWNLOAD_BASE}/latest"); then
            return 0
        else
            status=$?
        fi
        [ "$status" -eq 28 ] || return "$status"
        GITHUB_TIMED_OUT=1
        log_warn "GitHub 查询超时，正在探测备用下载源..." >&2
    fi
    probe_release_mirrors "$RELEASE_METADATA_URL" latest || return 1
    log_info "使用镜像源 ${RANKED_MIRRORS[0]}（HTTPS 响应 ${RANKED_MIRROR_TIMES[0]} 秒）" >&2
    tag=$(mirror_release_tag "${RANKED_MIRROR_FILES[0]}") || { cleanup_mirror_probe; return 1; }
    LATEST_RELEASE_URL="${DEFAULT_DOWNLOAD_BASE}/tag/${tag}"
    cleanup_mirror_probe
}

download_release_file() {
    local url="$1" destination="$2" status index source checksum_url kind=checksum
    if [ "$GITHUB_TIMED_OUT" -eq 0 ]; then
        if curl -q -fsSL --connect-timeout 10 --max-time 120 --max-filesize 536870912 "$url" -o "$destination.part"; then
            if [[ "$url" != */SHA256SUMS ]] || grep -Eq '^[[:xdigit:]]{64}[[:blank:]]+\*?[^[:space:]]' "$destination.part"; then
                mv "$destination.part" "$destination"
                return
            fi
            status=22
        else
            status=$?
        fi
        rm -f "$destination.part"
        [ "$status" -ne 23 ] || return "$status"
        if [ "$status" -eq 28 ]; then
            GITHUB_TIMED_OUT=1
            log_warn "GitHub 下载超时，正在探测备用下载源..." >&2
        elif [[ "$url" != */SHA256SUMS ]]; then
            return "$status"
        fi
    fi
    checksum_url="${url%/*}/SHA256SUMS"
    if [ "$url" != "$checksum_url" ]; then
        checksum_url="$url"
        kind=artifact
    fi
    if ! probe_release_mirrors "$checksum_url" "$kind"; then
        [[ "$url" != */SHA256SUMS ]] || return 69
        return 1
    fi
    for index in "${!RANKED_MIRRORS[@]}"; do
        source="${RANKED_MIRRORS[$index]}"
        log_info "使用镜像源 ${source}（HTTPS 响应 ${RANKED_MIRROR_TIMES[$index]} 秒）" >&2
        if [[ "$url" = */SHA256SUMS ]]; then
            if mv "${RANKED_MIRROR_FILES[$index]}" "$destination.part" && mv "$destination.part" "$destination"; then
                cleanup_mirror_probe
                return 0
            fi
            rm -f "$destination.part"
            cleanup_mirror_probe
            return 1
        fi
        if curl -q -fsSL --connect-timeout 10 --max-time 120 --max-filesize 536870912 "${source}${url}" -o "$destination.part"; then
            mv "$destination.part" "$destination" || { cleanup_mirror_probe; return 1; }
            cleanup_mirror_probe
            return 0
        fi
        rm -f "$destination.part"
    done
    cleanup_mirror_probe
    log_error "所有可用镜像下载均失败" >&2
    return 1
}

resolve_download_url() {
    local artifact="$1"
    if [ "$RELEASE_VERSION" = "latest" ]; then
        DOWNLOAD_URL="${DEFAULT_DOWNLOAD_BASE}/latest/download/${artifact}"
    else
        DOWNLOAD_URL="${DEFAULT_DOWNLOAD_BASE}/download/${RELEASE_VERSION}/${artifact}"
    fi
}

download_release_checksums() {
    [ "$CHECKSUMS_UNAVAILABLE" -eq 0 ] || return 0
    local checksums="$TMP_DIR/SHA256SUMS"
    local status
    if [ ! -f "$checksums" ]; then
        if [ "$RELEASE_VERSION" = latest ]; then
            query_latest_release || return 1
            RELEASE_VERSION="${LATEST_RELEASE_URL##*/}"
            [[ "$LATEST_RELEASE_URL" = */tag/* ]] && upgrade_version_key "$RELEASE_VERSION" >/dev/null || return 1
        fi
        resolve_download_url "SHA256SUMS"
        log_step "下载发布校验清单：${DOWNLOAD_URL}"
        if download_release_file "$DOWNLOAD_URL" "$checksums"; then
            return 0
        else
            status=$?
        fi
        if [ "$status" -eq 69 ]; then
            CHECKSUMS_UNAVAILABLE=1
            log_warn "所有来源均无法获取校验清单，跳过 SHA-256 校验并继续安装或升级。" >&2
            return 0
        fi
        log_error "无法保存发布校验清单：${DOWNLOAD_URL}"
        return "$status"
    fi
}

verify_release_checksum() {
    local file="$1" artifact="$2" checksums="$TMP_DIR/SHA256SUMS"
    download_release_checksums || return $?
    [ "$CHECKSUMS_UNAVAILABLE" -eq 0 ] || return 0
    local expected
    local actual
    expected=$(awk -v name="$artifact" '$2 == name || $2 == "*" name { print $1; exit }' "$checksums")
    if [ -z "$expected" ]; then
        log_error "校验清单中没有 ${artifact} 的记录"
        exit 1
    fi
    actual=$(sha256sum "$file" | awk '{ print $1 }')
    if [ "$actual" != "$expected" ]; then
        log_error "SHA-256 校验不匹配：${artifact}"
        exit 1
    fi
    log_info "SHA-256 校验通过：${artifact}"
}

validate_staged_version() {
    local report="$1" program actual rest actual_key target_key
    read -r program actual rest <<< "$report"
    case "$program" in yz-agent|agent|xboard-node|xbctl) ;; *) log_error "程序版本信息无效"; return 1 ;; esac
    actual_key=$(upgrade_version_key "$actual") || { log_error "程序版本信息无效"; return 1; }
    if [ "$RELEASE_VERSION" != latest ] && [ -n "$RELEASE_VERSION" ]; then
        target_key=$(upgrade_version_key "$RELEASE_VERSION") || return 1
        if [ "$actual_key" != "$target_key" ]; then
            log_error "程序版本 $actual 与指定版本 $RELEASE_VERSION 不一致"
            return 1
        fi
    fi
}

stage_binary() {
    local staged="$BIN_STAGE_DIR/xboard-node"
    if [ "${UPGRADE_FROM_RELEASE:-0}" -ne 1 ] && [ -n "$BINARY_SOURCE" ]; then
        if [ ! -f "$BINARY_SOURCE" ]; then
            log_error "未找到本地程序：$BINARY_SOURCE" >&2
            return 1
        fi
        log_info "使用本地程序：${BINARY_SOURCE}"
        if [ "${YZ_INSTALL_PARENT_LOCK:-}" = 1 ]; then
            ln -- "$BINARY_SOURCE" "$staged"
        else
            cp -- "$BINARY_SOURCE" "$staged"
        fi
    else
        download_release_checksums || return $?
        local artifact="" name downloaded=0
        local candidates=()
        for name in yz-agent agent xboard-node; do
            if [ "$CHECKSUMS_UNAVAILABLE" -eq 1 ]; then
                candidates+=("$name-linux-$ARCH")
            elif awk -v name="$name-linux-$ARCH" '$2 == name || $2 == "*" name { found=1 } END { exit !found }' "$TMP_DIR/SHA256SUMS"; then
                artifact="$name-linux-$ARCH"
                candidates+=("$artifact")
                break
            fi
        done
        if [ "${#candidates[@]}" -eq 0 ]; then
            log_error "发布校验清单中没有受支持的节点安装包"
            return 1
        fi
        for artifact in "${candidates[@]}"; do
            resolve_download_url "$artifact"
            log_step "下载节点程序：${DOWNLOAD_URL}"
            if download_release_file "$DOWNLOAD_URL" "$staged"; then downloaded=1; break; fi
        done
        if [ "$downloaded" -eq 0 ]; then
            log_error "节点程序下载失败：${DOWNLOAD_URL}"
            exit 1
        fi
        verify_release_checksum "$staged" "$artifact"
    fi
    chmod 755 "$staged"
    local report
    if ! report=$("$staged" -v); then
        log_error "节点程序版本检查失败"
        exit 1
    fi
    validate_staged_version "$report" || return 1
    read -r _ INSTALLED_VERSION _ <<< "$report"
    log_info "程序版本检查通过：${INSTALLED_VERSION}"
    select_target_name "$report"
}

stage_xbctl() {
    local staged="$BIN_STAGE_DIR/xbctl"
    if [ "$APP_NAME" != xboard-node ]; then
        # 统一程序同时提供管理命令，只保留一个文件内容副本。
        ln "$BIN_STAGE_DIR/xboard-node" "$staged"
        "$staged" config bin-dir >/dev/null
        return
    fi
    if [ "${UPGRADE_FROM_RELEASE:-0}" -ne 1 ] && [ -n "$CLI_BINARY_SOURCE" ]; then
        if [ ! -f "$CLI_BINARY_SOURCE" ]; then
            log_error "未找到历史版管理程序：$CLI_BINARY_SOURCE" >&2
            return 1
        fi
        log_info "使用本地历史版管理程序：${CLI_BINARY_SOURCE}"
        if [ "${YZ_INSTALL_PARENT_LOCK:-}" = 1 ]; then
            ln -- "$CLI_BINARY_SOURCE" "$staged"
        else
            cp -- "$CLI_BINARY_SOURCE" "$staged"
        fi
    else
        local artifact="xbctl-linux-${ARCH}"
        resolve_download_url "$artifact"
        log_step "下载历史版管理程序：${DOWNLOAD_URL}"
        if ! download_release_file "$DOWNLOAD_URL" "$staged"; then
            log_error "历史版管理程序下载失败：${DOWNLOAD_URL}"
            exit 1
        fi
        verify_release_checksum "$staged" "$artifact"
    fi
    chmod 755 "$staged"
    local report
    if ! report=$("$staged" version); then
        log_error "历史版管理程序版本检查失败"
        exit 1
    fi
    validate_staged_version "$report" || return 1
    if [ "$BIN_DIR" != "$DEFAULT_BIN_DIR" ] && ! "$staged" config bin-dir >/dev/null 2>&1; then
        log_error "该版本不支持自定义程序目录，请先迁回 $DEFAULT_BIN_DIR 再降级"
        exit 1
    fi
}

render_config() {
    local config_helper="$BIN_STAGE_DIR/xbctl"
    local init_args=(
        config init
        --mode "$MODE"
        --panel-url "$PANEL_URL"
        --health-port "${HEALTH_PORT:-0}"
        --version "$RELEASE_VERSION"
        --output "$TMP_DIR/config.yml"
        --credentials-out "$TMP_DIR/credentials.env"
        --meta "$TMP_DIR/install-meta.json"
        --install-root "${TARGET_INSTALL_ROOT:-$INSTALL_ROOT}"
    )
    # 不把默认值伪装成显式选择，交给 xbctl 区分新绑定与已有实例。
    if [ "$KERNEL_EXPLICIT" -eq 1 ]; then
        init_args+=(--kernel "$KERNEL_TYPE")
    fi
    if [ -f "$CONFIG_FILE" ]; then
        init_args+=(--config "$CONFIG_FILE")
    fi
    if [ -f "$CREDENTIALS_FILE" ]; then
        init_args+=(--credentials-in "$CREDENTIALS_FILE")
    fi
    if [ "$MODE" = "machine" ]; then
        init_args+=(--machine-id "$MACHINE_ID")
    else
        init_args+=(--node-id "$NODE_ID")
        if [ -n "$NODE_TYPE" ]; then
            init_args+=(--node-type "$NODE_TYPE")
        fi
    fi
    if [ -n "$RUNTIME_GOMEMLIMIT" ]; then
        init_args+=(--gomemlimit "$RUNTIME_GOMEMLIMIT")
    fi
    if [ -n "$RUNTIME_GOGC" ] && [ "$RUNTIME_GOGC" -gt 0 ] 2>/dev/null; then
        init_args+=(--gogc "$RUNTIME_GOGC")
    fi

    # 旧版无法直接读取加密配置，由已安装的新程序生成兼容格式。
    PRIVATE_LEGACY_EXPORT=0
    if ! "$config_helper" config init --supports-private-storage >/dev/null 2>&1 &&
        [ -x "$PREVIOUS_BINARY_PATH" ] &&
        "$PREVIOUS_BINARY_PATH" config init --supports-private-storage >/dev/null 2>&1; then
        config_helper="$PREVIOUS_BINARY_PATH"
        init_args+=(--legacy-output)
        PRIVATE_LEGACY_EXPORT=1
    fi
    # 新版从标准输入接收凭据，避免令牌出现在进程参数中；历史版本保留原兼容方式。
    if "$config_helper" config init --supports-token-stdin >/dev/null 2>&1; then
        init_args+=(--token-stdin)
    else
        init_args+=(--token "$TOKEN")
    fi
    local output
    output=$("$config_helper" "${init_args[@]}" <<<"$TOKEN") || {
        log_error "生成安装配置失败"
        exit 1
    }

    INSTANCE_ID=$(echo "$output" | grep '^INSTANCE_ID=' | cut -d= -f2-)
    chmod 600 "$TMP_DIR/credentials.env"
}

render_service() {
    local INSTALL_ROOT="${TARGET_INSTALL_ROOT:-$INSTALL_ROOT}"
    local CONFIG_FILE="$INSTALL_ROOT/config.yml" CREDENTIALS_FILE="$INSTALL_ROOT/credentials.env"
    if [ "$SERVICE_MANAGER" = "openrc" ]; then
        cat >"$TMP_DIR/service" <<EOF_OPENRC
#!/sbin/openrc-run

name="YZ-Agent"
description="YZ-Agent node backend"
supervisor=supervise-daemon
command="${BINARY_PATH}"
command_args="${RUN_COMMAND}-c ${CONFIG_FILE}"
directory="${INSTALL_ROOT}"
pidfile="/run/${SERVICE_NAME}.pid"
output_log="${OPENRC_LOG_PATH}"
error_log="${OPENRC_LOG_PATH}"
respawn_delay=5
respawn_max=0
retry="TERM/150/KILL/5"
no_new_privs=true
rc_ulimit="-n 1048576"

depend() {
    need net localmount
}

start_pre() {
    local line key value
    if [ -r "${CREDENTIALS_FILE}" ]; then
        while IFS= read -r line || [ -n "\$line" ]; do
            case "\$line" in
                ""|\#*) continue ;;
            esac
            key=\${line%%=*}
            if [ "\$key" = "\$line" ]; then
                eerror "凭据记录无效：缺少等号"
                return 1
            fi
            case "\$key" in
                ""|[0-9]*|*[!A-Za-z0-9_]*)
                    eerror "凭据名称无效：\$key"
                    return 1
                    ;;
            esac
            value=\${line#*=}
            export "\${key}=\${value}"
        done < "${CREDENTIALS_FILE}"
    fi
    checkpath -f -m 0640 -o root:root "${OPENRC_LOG_PATH}"
}
EOF_OPENRC
        return
    fi

    cat >"$TMP_DIR/service" <<EOF_UNIT
[Unit]
Description=YZ-Agent
Documentation=https://github.com/P0me1oo/YZ-Agent
After=network-online.target
Wants=network-online.target
RequiresMountsFor=${BIN_DIR:-/usr/local/bin}

[Service]
Type=simple
WorkingDirectory=${INSTALL_ROOT}
EnvironmentFile=-${CREDENTIALS_FILE}
ExecStart=${BINARY_PATH} ${RUN_COMMAND}-c ${CONFIG_FILE}
Restart=always
RestartSec=5
TimeoutStopSec=150s
LimitNOFILE=1048576
NoNewPrivileges=true
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF_UNIT
}

backup_existing_state() {
    local snapshot item source name
    BACKUP_PENDING=$(mktemp -d "$BACKUP_DIR/install-XXXXXX")
    snapshot="$BACKUP_PENDING"
    for item in config.yml credentials.env install-meta.json bin-dir install.sh service; do
        case "$item" in
            config.yml) source="$CONFIG_FILE" ;;
            credentials.env) source="$CREDENTIALS_FILE" ;;
            install-meta.json) source="$INSTALL_META" ;;
            bin-dir) source="$BIN_DIR_FILE" ;;
            install.sh) source="$INSTALLER_COPY_PATH" ;;
            service) source="${PREVIOUS_SERVICE_PATH:-$SERVICE_PATH}" ;;
        esac
        if [ -f "$source" ]; then
            cp "$source" "$snapshot/$item"
        fi
    done
    if [ -f "${PREVIOUS_SERVICE_PATH:-$SERVICE_PATH}" ]; then SERVICE_EXISTED=1; else SERVICE_EXISTED=0; fi
    if [ "$BIN_DIR" = "$PREVIOUS_BIN_DIR" ]; then
        local names=("$APP_NAME")
        if [ "$CLI_NAME" != "$APP_NAME" ]; then names+=("$CLI_NAME"); fi
        for name in "${names[@]}"; do
            source="$BIN_DIR/$name"
            if [ -e "$source" ] || [ -L "$source" ]; then
                # 硬链接保留原文件，不复制二进制内容；替换时必须使用同分区重命名。
                ln "$source" "$BIN_STAGE_DIR/previous-$name"
            fi
        done
    fi
    if [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ] || [ "$CLI_PATH" != "$CLI_SYMLINK_PATH" ]; then
        if [ -e "$CLI_SYMLINK_PATH" ] || [ -L "$CLI_SYMLINK_PATH" ]; then
            CLI_ENTRY_BACKUP_DIR=$(mktemp -d "$(dirname "$CLI_SYMLINK_PATH")/.yz-agent-entry.XXXXXX")
            ln "$CLI_SYMLINK_PATH" "$CLI_ENTRY_BACKUP_DIR/xbctl"
        fi
    fi
    # 所有快照完成后才允许错误处理执行恢复，避免备份失败时删除原文件。
    BACKUP_PATH="$snapshot"
    BACKUP_PENDING=""
}

replace_binary_files() {
    BINARIES_CHANGED=1
    # 在重命名前登记恢复范围，覆盖命令完成后立即收到中断信号的情况。
    BINARY_REPLACE_STARTED=1
    mv -f "$BIN_STAGE_DIR/xboard-node" "$BINARY_PATH"
    if [ "$CLI_PATH" = "$CLI_SYMLINK_PATH" ] && [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ]; then
        CLI_ENTRY_CHANGED=1
    fi
    if [ "$CLI_PATH" != "$BINARY_PATH" ]; then
        CLI_REPLACE_STARTED=1
        mv -f "$BIN_STAGE_DIR/xbctl" "$CLI_PATH"
    fi
    if [ "$CLI_PATH" != "$CLI_SYMLINK_PATH" ]; then
        CLI_ENTRY_CHANGED=1
        ln -sfn "$CLI_PATH" "$CLI_SYMLINK_PATH"
    fi
    printf '%s\n' "$BIN_DIR" >"$TMP_DIR/bin-dir"
    BIN_DIR_RECORD_TMP=$(mktemp "$BIN_DIR_FILE.XXXXXX")
    install -m 644 "$TMP_DIR/bin-dir" "$BIN_DIR_RECORD_TMP"
    mv -f "$BIN_DIR_RECORD_TMP" "$BIN_DIR_FILE"
}

finish_install() {
    if [ "$NAME_CHANGED" -eq 1 ] && [ -f "$PREVIOUS_SERVICE_PATH" ]; then
        if [ "$OLD_SERVICE_ENABLED" -eq 1 ]; then
            OLD_SERVICE_DISABLED=1
            case "$SERVICE_MANAGER" in
                systemd) systemctl disable "$PREVIOUS_SERVICE_NAME.service" ;;
                openrc) rc-update del "$PREVIOUS_SERVICE_NAME" default ;;
            esac
        fi
        rm -f "$PREVIOUS_SERVICE_PATH"
        service_reload
    fi
    if [ "$ROOT_MIGRATION_STARTED" -eq 1 ] && [ -L "$PREVIOUS_INSTALL_ROOT" ] &&
       [ "$(readlink "$PREVIOUS_INSTALL_ROOT")" = "$TARGET_INSTALL_ROOT" ]; then
        rm "$PREVIOUS_INSTALL_ROOT"
    fi
    # 新服务验证成功后才删除原目录中的程序，其他文件全部保留。
    INSTALL_COMMITTED=1
    if [ "$PREVIOUS_CLI_ENTRY_PATH" != "$CLI_SYMLINK_PATH" ] && [ -L "$PREVIOUS_CLI_ENTRY_PATH" ] &&
       [ "$(readlink "$PREVIOUS_CLI_ENTRY_PATH")" = "$PREVIOUS_CLI_PATH" ]; then
        rm -f "$PREVIOUS_CLI_ENTRY_PATH" || log_warn "无法移除旧命令入口"
    fi
    if [ "$PREVIOUS_CLI_PATH" != "$CLI_PATH" ] && [ "$PREVIOUS_CLI_PATH" != "$BINARY_PATH" ] &&
       [ "$PREVIOUS_CLI_PATH" != "$CLI_SYMLINK_PATH" ]; then
        rm -f "$PREVIOUS_CLI_PATH" || log_warn "无法移除旧管理程序"
    fi
    if [ "$BINARY_PATH" != "$PREVIOUS_BINARY_PATH" ] && [ "$BIN_DIR" = "$PREVIOUS_BIN_DIR" ]; then
        rm -f "$PREVIOUS_BINARY_PATH" || log_warn "无法移除旧程序：$PREVIOUS_BINARY_PATH"
    fi
    if [ "$BIN_DIR" != "$PREVIOUS_BIN_DIR" ]; then
        if [ "$PREVIOUS_BINARY_PATH" != "$CLI_SYMLINK_PATH" ]; then
            if ! rm -f "$PREVIOUS_BINARY_PATH"; then
                log_warn "新安装已运行，但无法移除旧程序：$PREVIOUS_BINARY_PATH"
            fi
        fi
        if [ "$PREVIOUS_CLI_PATH" != "$CLI_SYMLINK_PATH" ] && [ "$PREVIOUS_CLI_PATH" != "$BINARY_PATH" ]; then
            if ! rm -f "$PREVIOUS_CLI_PATH"; then
                log_warn "新安装已运行，但无法移除旧管理程序：$PREVIOUS_CLI_PATH"
            fi
        fi
    fi
    log_info "程序已安装到：$BIN_DIR"
}

stop_existing_service() {
    if [ -f "$SERVICE_PATH" ] || service_is_active; then
        service_stop >/dev/null 2>&1
    fi
}

install_staged_files() {
    stop_previous_service
    stop_existing_service
    cleanup_installation_firewall
    if [ "$PRIVATE_LEGACY_EXPORT" -eq 1 ] && [ -f "$CONFIG_FILE" ]; then
        "$PREVIOUS_BINARY_PATH" config private-export --config "$CONFIG_FILE"
    fi
    migrate_install_root
    replace_binary_files
    install -m 600 "$TMP_DIR/config.yml" "$CONFIG_FILE"
    install -m 600 "$TMP_DIR/credentials.env" "$CREDENTIALS_FILE"
    install -m 600 "$TMP_DIR/install-meta.json" "$INSTALL_META"
    if [ -f "$0" ] && [ "$(realpath "$0")" != "$(realpath "$INSTALLER_COPY_PATH" 2>/dev/null || echo "$INSTALLER_COPY_PATH")" ]; then
        install -m 755 "$0" "$INSTALLER_COPY_PATH"
    fi
    install -m "$(service_file_mode)" "$TMP_DIR/service" "$SERVICE_PATH"
    service_reload
    service_enable > /dev/null 2>&1
}

wait_for_health() {
    if ! service_is_active; then
        return 1
    fi
    if [ "$HEALTH_ENABLED" -eq 0 ]; then
        return 0
    fi
    log_info "等待服务就绪（最多 ${HEALTH_WAIT_SECONDS} 秒）"
    local deadline=$((SECONDS + HEALTH_WAIT_SECONDS))
    local remaining request_timeout
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! service_is_active; then
            return 1
        fi
        remaining=$((deadline - SECONDS))
        [ "$remaining" -gt 0 ] || return 1
        request_timeout=2
        [ "$remaining" -ge "$request_timeout" ] || request_timeout="$remaining"
        if curl --noproxy '*' --connect-timeout "$request_timeout" --max-time "$request_timeout" \
            -fsS "http://127.0.0.1:${HEALTH_PORT}/healthz" >/dev/null 2>&1; then
            return 0
        fi
        if [ "$SECONDS" -lt "$deadline" ]; then
            sleep 1
        fi
    done
    return 1
}

show_recent_logs() {
    if [ "$SERVICE_MANAGER" = "systemd" ] && command -v journalctl >/dev/null 2>&1; then
        journalctl -u "$SYSTEMD_SERVICE_NAME" -n 30 --no-pager || true
    elif [ "$SERVICE_MANAGER" = "openrc" ] && [ -f "$OPENRC_LOG_PATH" ]; then
        tail -n 30 "$OPENRC_LOG_PATH" || true
    fi
}

start_service() {
    REPLACEMENT_SERVICE_STARTED=1
    if service_is_active; then
        service_restart
    else
        service_start
    fi
    if ! wait_for_health; then
        log_error "服务未通过健康检查"
        show_recent_logs
        return 1
    fi
}

perform_install() {
    validate_install_request
    detect_current_state
    require_reconfigure_confirmation
    TMP_DIR=$(mktemp -d)
    ensure_dirs
    log_step "准备安装程序"
    stage_binary
    stage_xbctl
    select_target_root
    log_step "生成配置和服务文件"
    render_config
    prepare_root_config
    render_service
    log_step "保存恢复文件并安装"
    backup_existing_state
    install_staged_files
    load_health_port_from_config "$CONFIG_FILE" "$CLI_PATH"
    log_step "启动服务并检查运行状态"
    start_service
    finish_install

    show_completion "安装"
}

# 与 Go 升级入口保持一致：历史两段版本补零，yz 修订按数字排序。
upgrade_version_key() {
    local value="$1" part
    if ! [[ "$value" =~ ^v?([0-9]+)\.([0-9]+)(\.([0-9]+))?(-yz\.([0-9]+))?$ ]]; then
        return 1
    fi
    local major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}"
    local patch="${BASH_REMATCH[4]:-0}" revision="${BASH_REMATCH[6]}" stable=1
    for part in "$major" "$minor" "$patch" "${revision:-0}"; do
        [[ "$part" = 0 || "$part" != 0* ]] || return 1
    done
    [ -z "$revision" ] || stable=0
    printf '%s.%s.%s.%s.%s\n' "$major" "$minor" "$patch" "$stable" "${revision:-0}"
}

check_latest_upgrade() {
    UPGRADE_SKIPPED=0
    UPGRADE_FROM_RELEASE=0
    [ "$RELEASE_VERSION" = latest ] || return 0
    local report current current_key target target_key url first
    if ! report=$("${PREVIOUS_BINARY_PATH:-$BINARY_PATH}" -v); then
        log_error "读取当前版本失败，已停止升级"
        return 1
    fi
    read -r _ current _ <<< "$report"
    if ! current_key=$(upgrade_version_key "$current"); then
        log_error "无法识别当前版本：$current，已停止升级"
        return 1
    fi
    if ! query_latest_release; then
        log_error "查询最新正式版失败，已停止升级"
        return 1
    fi
    url="$LATEST_RELEASE_URL"
    target="${url##*/}"
    if [[ "$url" != */tag/* ]] || ! target_key=$(upgrade_version_key "$target"); then
        log_error "无法识别最新正式版：$target，已停止升级"
        return 1
    fi
    if [ "$current_key" = "$target_key" ]; then
        log_info "已是最新版本"
        UPGRADE_SKIPPED=1
        return 0
    fi
    first=$(printf '%s\n%s\n' "$current_key" "$target_key" | LC_ALL=C sort -V | head -n 1)
    if [ "$first" = "$target_key" ]; then
        log_info "当前版本 $current 高于最新正式版 $target，不自动降级"
        UPGRADE_SKIPPED=1
        return 0
    fi
    RELEASE_VERSION="$target"
    UPGRADE_FROM_RELEASE=1
}

perform_upgrade() {
    detect_current_state
    if [ "$CURRENT_STATE" = "fresh" ]; then
        log_error "未找到已安装程序，无法读取当前版本，已停止升级；首次安装请使用 install"
        return 1
    fi
    check_latest_upgrade || return 1
    [ "$UPGRADE_SKIPPED" -eq 0 ] || return 0
    if [ "$UPGRADE_FROM_RELEASE" -eq 1 ] && [ "${YZ_INSTALL_PARENT_LOCK:-}" != 1 ]; then install_dependencies; fi
    load_health_port_from_config "$CONFIG_FILE"
    TMP_DIR=$(mktemp -d)
    ensure_dirs
    log_step "准备升级程序"
    stage_binary
    stage_xbctl
    select_target_root
    log_step "生成服务文件"
    prepare_root_config
    render_service
    log_step "保存恢复文件并替换程序"
    backup_existing_state
    stop_previous_service
    stop_existing_service
    cleanup_installation_firewall
    migrate_install_root
    replace_binary_files
    load_health_port_from_config "$CONFIG_FILE" "$CLI_PATH"
    install -m "$(service_file_mode)" "$TMP_DIR/service" "$SERVICE_PATH"
    service_reload
    if [ "$NAME_CHANGED" -eq 1 ] && [ "$OLD_SERVICE_ENABLED" -eq 1 ]; then service_enable; fi
    REPLACEMENT_SERVICE_STARTED=1
    log_step "重启服务并检查运行状态"
    service_restart
    if ! wait_for_health; then
        log_error "升级后服务未通过健康检查"
        show_recent_logs
        return 1
    fi
    "$CLI_PATH" config refresh-meta \
        --config "$CONFIG_FILE" \
        --meta "$INSTALL_META" \
        --version "$RELEASE_VERSION"
    finish_install
    show_completion "升级"
}

confirm_uninstall() {
    if [ "$YES" -eq 1 ]; then
        return
    fi
    echo
    read -r -p "确认卸载？[y/N]：" answer
    if ! [[ "$answer" =~ ^[Yy]$ ]]; then
        log_warn "已取消卸载"
        exit 0
    fi
}

perform_uninstall() {
    confirm_uninstall
    log_step "停止服务并移除程序"
    if [ -f "$SERVICE_PATH" ]; then
        service_stop >/dev/null 2>&1 || return 1
    fi
    if [ "$APP_NAME" = yz-agent ] && [ -x "$BINARY_PATH" ]; then
        FIREWALL_CLEANUP_PROGRAM="$BINARY_PATH"
        cleanup_installation_firewall || return 1
    fi
    if [ -f "$SERVICE_PATH" ]; then
        service_disable >/dev/null 2>&1 || true
        rm -f "$SERVICE_PATH"
        service_reload || true
    fi
    rm -f "$BINARY_PATH"
    rm -f "$CLI_PATH"
    rm -f "$CLI_SYMLINK_PATH" 2>/dev/null || true
    rm -f "$BIN_DIR_FILE"
    if [ "$PURGE" -eq 1 ]; then
        rm -rf "$INSTALL_ROOT"
        log_info "已删除安装数据目录：${INSTALL_ROOT}"
    else
        rm -f "$INSTALL_META"
        log_info "配置已保留在：${INSTALL_ROOT}"
    fi
    show_banner "YZ-Agent 卸载完成"
}

perform_status() {
    detect_current_state
    local state label key val
    case "$CURRENT_STATE" in fresh) state=未安装 ;; installed) state=已安装 ;; partial) state=安装不完整 ;; esac
    show_banner "YZ-Agent 安装状态"
    log_config "状态：${state}"
    log_config "程序目录：${BIN_DIR}"
    if [ -f "$INSTALL_META" ]; then
        log_config "安装记录：${INSTALL_META}"
        if [ -x "$CLI_PATH" ]; then
            "$CLI_PATH" list 2>/dev/null || true
        else
            # Simple key extraction from JSON (no Python needed)
            for key in config_mode version latest_instance_id instance_count updated_at; do
                case "$key" in
                    config_mode) label=部署模式 ;;
                    version) label=版本 ;;
                    latest_instance_id) label=最近实例 ;;
                    instance_count) label=实例数量 ;;
                    updated_at) label=更新时间 ;;
                esac
                val=$(sed -n "s/.*\"${key}\": *\"\{0,1\}\([^\"]*\)\"\{0,1\}.*/\1/p" "$INSTALL_META" | head -1)
                val="${val%,}"  # strip trailing comma from numeric JSON values
                [ -n "$val" ] && log_config "${label}：${val}"
            done
        fi
    fi
    if [ -f "$SERVICE_PATH" ]; then
        log_config "服务：${SERVICE_NAME}"
        log_config "服务管理器：${SERVICE_MANAGER}"
        service_status || true
    fi
}

main() {
    parse_args "$@" || return 1
    case "$ACTION" in
        help)
            usage
            exit 0
            ;;
        status)
            select_install_root
            load_install_paths
            detect_service_manager
            perform_status
            exit 0
            ;;
    esac

    # 无效安装请求在创建锁、安装依赖等系统操作之前报错。
    if [ "$ACTION" = install ]; then validate_install_request; fi
    check_root
    select_install_root
    lock_installation
    load_install_paths
    detect_arch
    detect_os
    detect_service_manager
    show_install_config
    if [ "${YZ_INSTALL_PARENT_LOCK:-}" != 1 ] && \
       { [ "$ACTION" = install ] || { [ "$ACTION" = upgrade ] && [ "$RELEASE_VERSION" != latest ]; }; }; then
        install_dependencies
    fi

    case "$ACTION" in
        install)
            perform_install
            ;;
        upgrade)
            perform_upgrade
            ;;
        uninstall)
            perform_uninstall
            ;;
        *)
            log_error "不支持的操作；请使用 --help 查看用法"
            usage
            exit 1
            ;;
    esac
}

main "$@"
