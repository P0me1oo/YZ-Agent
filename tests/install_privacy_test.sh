#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST_PARENT=$(cd "${TMPDIR:-/tmp}" && pwd -P)
TEST_ROOT=$(mktemp -d "$TEST_PARENT/yz-agent-install-privacy.XXXXXX")
cleanup() {
    case "$TEST_ROOT" in
        "$TEST_PARENT"/yz-agent-install-privacy.*) rm -rf -- "$TEST_ROOT" ;;
        *) echo "拒绝清理范围外的测试目录" >&2; return 1 ;;
    esac
}
trap cleanup EXIT
sed '/^main "$@"$/d' "$REPO_ROOT/install.sh" >"$TEST_ROOT/library.sh"
mkdir -p "$TEST_ROOT/bin"
cat >"$TEST_ROOT/bin/xbctl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == 'config init --supports-token-stdin' ]]; then
    [[ "$MOCK_PRIVATE_INPUT" == 1 ]]
    exit $?
fi
printf '%s\n' "$@" >"$MOCK_ARGS"
from_stdin=0
received=''
credentials=''
while [[ $# -gt 0 ]]; do
    case "$1" in
        --token-stdin) from_stdin=1 ;;
        --token) received="$2"; shift ;;
        --credentials-out) credentials="$2"; shift ;;
    esac
    shift
done
if [[ "$from_stdin" == 1 ]]; then received=$(cat); fi
[[ "$received" == "$MOCK_EXPECTED_TOKEN" ]]
printf 'TEST_INSTALLATION=1\n' >"$credentials"
printf 'INSTANCE_ID=privacy-test\n'
MOCK
chmod 700 "$TEST_ROOT/bin/xbctl"

check_input() (
    local supported="$1"
    source "$TEST_ROOT/library.sh"
    trap - ERR EXIT INT TERM
    MODE=node
    PANEL_URL=https://privacy.example.invalid
    NODE_ID=1
    TOKEN="privacy-test-${RANDOM}-${RANDOM}"
    TMP_DIR="$TEST_ROOT/render-$supported"
    BIN_STAGE_DIR="$TEST_ROOT/bin"
    CONFIG_FILE="$TEST_ROOT/no-config.yml"
    CREDENTIALS_FILE="$TEST_ROOT/no-credentials.env"
    export MOCK_PRIVATE_INPUT="$supported" MOCK_EXPECTED_TOKEN="$TOKEN" MOCK_ARGS="$TEST_ROOT/args-$supported"
    mkdir -p "$TMP_DIR"
    render_config
    if [[ "$supported" == 1 ]]; then
        grep -Fx -- '--token-stdin' "$MOCK_ARGS" >/dev/null
        if grep -Fx -- '--token' "$MOCK_ARGS" >/dev/null || grep -F -- "$TOKEN" "$MOCK_ARGS" >/dev/null; then
            echo "新版安装器不得将认证凭据放入子进程参数" >&2
            exit 1
        fi
    else
        grep -Fx -- '--token' "$MOCK_ARGS" >/dev/null
    fi
    [[ "$(stat -c '%a' "$TMP_DIR/credentials.env")" == 600 ]]
)

check_input 1
check_input 0

# 显式降级时用现有新程序生成兼容配置。
mkdir -p "$TEST_ROOT/current"
cat >"$TEST_ROOT/current/yz-agent" <<'MOCK_CURRENT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == 'config init --supports-private-storage' || "$*" == 'config init --supports-token-stdin' ]]; then exit 0; fi
if [[ "$1 $2" == 'config private-export' ]]; then exit "${MOCK_EXPORT_FAIL:-0}"; fi
exec "$MOCK_RENDER_HELPER" "$@"
MOCK_CURRENT
chmod 700 "$TEST_ROOT/current/yz-agent"
cp "$TEST_ROOT/current/yz-agent" "$TEST_ROOT/current/custom-agent"
check_downgrade() (
    source "$TEST_ROOT/library.sh"
    trap - ERR EXIT INT TERM
    MODE=machine
    PANEL_URL=https://privacy.example.invalid
    MACHINE_ID=1
    TOKEN="privacy-test-${RANDOM}-${RANDOM}"
    TMP_DIR="$TEST_ROOT/downgrade-$1"
    BIN_STAGE_DIR="$TEST_ROOT/bin"
    PREVIOUS_BINARY_PATH="$TEST_ROOT/current/$1"
    CONFIG_FILE="$TEST_ROOT/no-config.yml"
    CREDENTIALS_FILE="$TEST_ROOT/no-credentials.env"
    export MOCK_PRIVATE_INPUT=1 MOCK_EXPECTED_TOKEN="$TOKEN" MOCK_ARGS="$TEST_ROOT/downgrade-args"
    export MOCK_RENDER_HELPER="$TEST_ROOT/bin/xbctl"
    mkdir -p "$TMP_DIR"
    render_config
    [[ "$PRIVATE_LEGACY_EXPORT" == 1 ]]
    grep -Fx -- '--legacy-output' "$MOCK_ARGS" >/dev/null
    grep -Fx -- '--token-stdin' "$MOCK_ARGS" >/dev/null
)
check_downgrade yz-agent
check_downgrade custom-agent

# 无法领取解密密钥时，失败回滚必须保留当前程序并重新启动它。
check_failed_private_rollback() (
    source "$TEST_ROOT/library.sh"
    trap - ERR EXIT INT TERM
    BINARY_REPLACE_STARTED=1
    REPLACEMENT_SERVICE_STARTED=0
    BINARY_PATH="$TEST_ROOT/current/yz-agent"
    BIN_STAGE_DIR="$TEST_ROOT/bin"
    BIN_DIR="$TEST_ROOT/current"
    PREVIOUS_BIN_DIR="$BIN_DIR"
    SERVICE_PATH="$TEST_ROOT/no-service"
    CONFIG_FILE="$TEST_ROOT/no-config.yml"
    export MOCK_EXPORT_FAIL=1
    service_is_active() { return 1; }
    service_start() { touch "$TEST_ROOT/current-restarted"; }
    if rollback_install; then echo "解密失败不应继续旧版回滚" >&2; exit 1; fi
    [[ "$ROLLBACK_FAILED" == 1 ]]
    [[ -x "$BINARY_PATH" && -f "$TEST_ROOT/current-restarted" ]]
)
check_failed_private_rollback
echo "安装凭据隐私及历史版本兼容测试通过"
