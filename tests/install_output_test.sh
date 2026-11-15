#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST_ROOT=$(mktemp -d /tmp/yz-agent-install-output.XXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT
sed '/^main "$@"$/d' "$REPO_ROOT/install.sh" >"$TEST_ROOT/library.sh"

# 输出格式可以调整，但真实认证信息和控制字符不能进入安装摘要。
(
    export NO_COLOR=1 TERM=xterm
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    OS=debian ARCH=amd64 SERVICE_MANAGER=systemd
    MODE=node NODE_ID=7
    PANEL_URL='https://summary.example.invalid/private?token=summary-url-secret'
    TOKEN='summary-token-secret'
    INSTALLED_VERSION=v2.37.3
    output=$(show_install_config; show_completion 安装; log_info 'literal\nvalue')
    if [[ "$output" == *"$TOKEN"* || "$output" == *"$PANEL_URL"* ]]; then
        echo '安装摘要泄露认证信息' >&2
        exit 1
    fi
    printf '%s\n' "$output"
) >"$TEST_ROOT/summary"
grep -Fq '[配置] 认证令牌：***' "$TEST_ROOT/summary"
grep -Fq '[配置] 版本：v2.37.3' "$TEST_ROOT/summary"
grep -Fq '（已通过）' "$TEST_ROOT/summary"
grep -Fxq '[信息] literal\nvalue' "$TEST_ROOT/summary"
if grep -Eq 'summary-(url|token)-secret' "$TEST_ROOT/summary" || grep -q $'\033' "$TEST_ROOT/summary"; then
    echo '安装摘要泄露认证信息或包含颜色控制字符' >&2
    exit 1
fi
(
    unset NO_COLOR
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    log_step '管道输出'
) | cat >"$TEST_ROOT/pipe"
if grep -q $'\033' "$TEST_ROOT/pipe"; then exit 1; fi
(
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    ACTION=install MODE=machine MACHINE_ID=3 SERVICE_MANAGER=openrc
    HEALTH_ENABLED=0 INSTALLED_VERSION=v1.13-yz.24
    show_install_config
    show_completion 升级
) >"$TEST_ROOT/disabled-health"
grep -Fq '机器模式（机器 3）' "$TEST_ROOT/disabled-health"
grep -Fq '健康检查：已禁用，仅确认服务运行' "$TEST_ROOT/disabled-health"
grep -Fq '版本：v1.13-yz.24' "$TEST_ROOT/disabled-health"
if grep -Fq '（已通过）' "$TEST_ROOT/disabled-health"; then exit 1; fi

# 缺少参数值或出现拼错的参数时，不能进入系统写操作。
for option in --mode --panel -a --api --token -t --node-id -n --node-type -T --machine-id --kernel -k --version --binary --xbctl-binary --bin-dir --health-port --gomemlimit --gogc; do
    if bash "$REPO_ROOT/install.sh" "$option" >"$TEST_ROOT/error" 2>&1; then exit 1; fi
    grep -Fq "参数 $option 缺少值" "$TEST_ROOT/error"
    if bash "$REPO_ROOT/install.sh" "$option" --help >"$TEST_ROOT/error" 2>&1; then exit 1; fi
    grep -Fq "参数 $option 缺少值" "$TEST_ROOT/error"
done
if bash "$REPO_ROOT/install.sh" --token -n 7 >"$TEST_ROOT/error" 2>&1; then exit 1; fi
grep -Fq '参数 --token 缺少值' "$TEST_ROOT/error"
if bash "$REPO_ROOT/install.sh" --versoin latest >"$TEST_ROOT/error" 2>&1; then exit 1; fi
grep -Fq '未知参数或操作' "$TEST_ROOT/error"
(
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    check_root() { echo '无效请求不应进入系统操作' >&2; exit 99; }
    main --panel https://request.example.invalid --node-id 1
) >"$TEST_ROOT/error" 2>&1 && exit 1
grep -Fq '请提供认证令牌' "$TEST_ROOT/error"
(
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    parse_args --panel https://args.example.invalid -t fake-token --machine-id 3 --bin-dir /opt/yz-agent
    [ "$MODE" = machine ] && [ "$TOKEN" = fake-token ] && [ "$BIN_DIR" = /opt/yz-agent ]
)

# 卸载无需安装依赖；调用入口而非实际操作系统服务。
(
    source "$TEST_ROOT/library.sh"
    trap - EXIT ERR INT TERM
    check_root() { :; }
    select_install_root() { :; }
    lock_installation() { :; }
    load_install_paths() { :; }
    detect_arch() { ARCH=amd64; }
    detect_os() { OS=debian; }
    detect_service_manager() { SERVICE_MANAGER=systemd; }
    install_dependencies() { echo '卸载不应安装依赖' >&2; exit 99; }
    perform_uninstall() { :; }
    main uninstall --yes
) >"$TEST_ROOT/uninstall"

echo '安装器输出与参数检查测试通过'
