#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source <(sed '/^main "$@"$/d' "$REPO_ROOT/install.sh")
trap - EXIT ERR
TEST_ROOT=$(mktemp -d)
trap 'cleanup_mirror_probe; rm -rf "$TEST_ROOT"' EXIT
log_info() { printf '%s\n' "$*" >&2; }
log_warn() { printf '%s\n' "$*" >&2; }
log_error() { printf '%s\n' "$*" >&2; }

RELEASE_MIRRORS=("https://slow.test/" "https://offline.test/" "https://fast.test/")
TEST_CHECKSUM="$(printf complete | sha256sum | awk '{print $1}')  yz-agent-linux-amd64"
DIRECT_MODE=timeout
FAST_FAIL=0
INVALID_MIRRORS=0
CHECKSUMS_MISSING=0

# 只模拟 HTTP 请求，不下载真实程序、不操作系统服务。
curl() {
    local url="" output="" format="" elapsed=0.010 data=""
    while [ "$#" -gt 0 ]; do
        case "$1" in
            -o) output="$2"; shift 2 ;;
            -w) format="$2"; shift 2 ;;
            --connect-timeout|--max-time|--max-filesize|--range) shift 2 ;;
            -q|-fsSL) shift ;;
            https://*) url="$1"; shift ;;
            *) printf '未预期的 curl 参数：%s\n' "$1" >&2; return 1 ;;
        esac
    done
    printf '%s\n' "$url" >> "$TEST_ROOT/requests"
    case "$url" in
        https://github.com/*)
            case "$DIRECT_MODE" in
                timeout) [ -z "$output" ] || printf partial > "$output"; return 28 ;;
                http) return 22 ;;
            esac
            if [[ "$url" = */latest ]]; then
                printf '%s/tag/v2.18.0' "$DEFAULT_DOWNLOAD_BASE"
                return
            fi
            ;;
        https://offline.test/*) return 28 ;;
        https://slow.test/*) elapsed=0.050 ;;
        https://fast.test/*) elapsed=0.005 ;;
        *) return 22 ;;
    esac
    if [[ "$url" = */SHA256SUMS ]] && [ "$CHECKSUMS_MISSING" -eq 1 ]; then return 22; fi
    if [[ "$url" = *api.github.com* ]]; then
        data='{"tag_name":"v2.18.0","draft":false,"prerelease":false}'
    elif [[ "$url" = */SHA256SUMS ]]; then
        data="$TEST_CHECKSUM"
    else
        if [[ "$url" = https://fast.test/* ]] && [ "$FAST_FAIL" -eq 1 ] && [ -z "$format" ]; then
            printf partial > "$output"
            return 22
        fi
        data=complete
    fi
    if [ "$INVALID_MIRRORS" -eq 1 ] && [[ "$url" != https://github.com/* ]]; then
        data='<html>Challenge</html>'
    fi
    [ -z "$output" ] || printf '%s' "$data" > "$output"
    [ "$format" != '%{time_total}' ] || printf '%s' "$elapsed"
    if [[ "$format" = *'%{http_code}'* ]]; then printf '%s\t200\tapplication/octet-stream' "$elapsed"; fi
    return 0
}

# 直连正常时不探测镜像。
DIRECT_MODE=success
download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/SHA256SUMS" "$TEST_ROOT/direct"
[ "$(wc -l < "$TEST_ROOT/requests")" -eq 1 ]
[ "$GITHUB_TIMED_OUT" -eq 0 ]

# 查询超时后并行筛选可用源，并优先选择响应最快者。
DIRECT_MODE=timeout
query_latest_release
[ "$LATEST_RELEASE_URL" = "$DEFAULT_DOWNLOAD_BASE/tag/v2.18.0" ]
[ "${RANKED_MIRRORS[*]}" = 'https://fast.test/ https://slow.test/' ]
[ "$GITHUB_TIMED_OUT" -eq 1 ]
[ -z "$MIRROR_PROBE_DIR" ]

# 后续清单和程序使用固定版本，不重复尝试已超时的直连。
download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/SHA256SUMS" "$TEST_ROOT/SHA256SUMS"
for attempt in 1 2; do
    download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/yz-agent-linux-amd64" "$TEST_ROOT/agent"
    [ "$(cat "$TEST_ROOT/agent")" = complete ]
    [ ! -e "$TEST_ROOT/agent.part" ]
done
[ "$(grep -c '^https://github.com/' "$TEST_ROOT/requests")" -eq 2 ]

# 最快源在实际下载时失败，会继续尝试下一源，部分文件会被覆盖和清理。
FAST_FAIL=1
download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/yz-agent-linux-amd64" "$TEST_ROOT/agent"
[ "$(cat "$TEST_ROOT/agent")" = complete ]
[ "$(tail -n 2 "$TEST_ROOT/requests" | head -n 1)" = 'https://fast.test/https://github.com/P0me1oo/YZ-Agent/releases/download/v2.18.0/yz-agent-linux-amd64' ]
[ "$(tail -n 1 "$TEST_ROOT/requests")" = 'https://slow.test/https://github.com/P0me1oo/YZ-Agent/releases/download/v2.18.0/yz-agent-linux-amd64' ]

# 全部返回无效内容时失败，旧文件保持不变，清理探测目录。
INVALID_MIRRORS=1
if query_latest_release; then exit 1; fi
if download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/yz-agent-linux-amd64" "$TEST_ROOT/agent"; then exit 1; fi
[ "$(cat "$TEST_ROOT/agent")" = complete ]
[ ! -e "$TEST_ROOT/agent.part" ]
[ -z "$MIRROR_PROBE_DIR" ]

# HTTP 错误保持原有行为，不当作超时。
GITHUB_TIMED_OUT=0 DIRECT_MODE=http
: > "$TEST_ROOT/requests"
if query_latest_release; then exit 1; fi
if download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/yz-agent-linux-amd64" "$TEST_ROOT/failed"; then exit 1; fi
[ "$(wc -l < "$TEST_ROOT/requests")" -eq 2 ]
[ ! -e "$TEST_ROOT/failed" ]
[ ! -e "$TEST_ROOT/failed.part" ]

# GitHub 与所有镜像均缺少清单时自动跳过，仍可直接探测、下载程序。
GITHUB_TIMED_OUT=0 DIRECT_MODE=timeout INVALID_MIRRORS=0 FAST_FAIL=0 CHECKSUMS_MISSING=1
CHECKSUMS_UNAVAILABLE=0 RELEASE_VERSION=v2.18.0 TMP_DIR="$TEST_ROOT/no-checksums"
mkdir -p "$TMP_DIR"
download_release_checksums
[ "$CHECKSUMS_UNAVAILABLE" -eq 1 ]
[ ! -e "$TMP_DIR/SHA256SUMS" ]
download_release_file "$DEFAULT_DOWNLOAD_BASE/download/v2.18.0/yz-agent-linux-amd64" "$TEST_ROOT/unverified-agent"
[ "$(cat "$TEST_ROOT/unverified-agent")" = complete ]
verify_release_checksum "$TEST_ROOT/unverified-agent" yz-agent-linux-amd64

# 清单缺失时仍兼容历史附件名，前两个名字不存在也会继续尝试。
(
    BIN_STAGE_DIR="$TEST_ROOT/legacy-stage" ARCH=amd64 UPGRADE_FROM_RELEASE=1
    mkdir -p "$BIN_STAGE_DIR"
    select_target_name() { :; }
    download_release_file() {
        [[ "$1" = */xboard-node-linux-amd64 ]] || return 22
        printf '#!/usr/bin/env bash\nprintf "xboard-node v1.13-yz.24\\n"\n' > "$2"
    }
    stage_binary
)

# 已拿到清单但哈希不匹配时仍停止，不能转为自动跳过。
if (
    CHECKSUMS_UNAVAILABLE=0
    download_release_checksums() { return 0; }
    printf '%s\n' "$TEST_CHECKSUM" > "$TMP_DIR/SHA256SUMS"
    printf corrupt > "$TEST_ROOT/corrupt-agent"
    verify_release_checksum "$TEST_ROOT/corrupt-agent" yz-agent-linux-amd64
); then exit 1; fi

# 本地文件写入失败不等于远程清单缺失。
(
    CHECKSUMS_UNAVAILABLE=0 TMP_DIR="$TEST_ROOT/save-failure"
    mkdir -p "$TMP_DIR"
    download_release_file() { return 23; }
    if download_release_checksums; then exit 1; fi
    [ "$CHECKSUMS_UNAVAILABLE" -eq 0 ]
)

# 草稿、预发布版本和缺失状态字段不能作为最新正式版。
for data in \
    '{"tag_name":"v2.18.0-beta.1","draft":false,"prerelease":true}' \
    '{"tag_name":"v2.18.0","draft":true,"prerelease":false}' \
    '{"tag_name":"v2.18.0"}'; do
    printf '%s\n' "$data" > "$TEST_ROOT/metadata"
    if mirror_release_tag "$TEST_ROOT/metadata"; then exit 1; fi
done

printf '%s\n' '下载镜像探测与回退测试通过'
