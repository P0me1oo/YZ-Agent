#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source <(sed '/^main "$@"$/d' "$REPO_ROOT/install.sh")
trap - EXIT ERR INT TERM

# 模拟命令缺失和 BusyBox，不操作真实包管理器或系统文件。
command() {
    if [ "${1:-}" = -v ] && [ "${2:-}" = "$missing_tool" ]; then return 1; fi
    builtin command "$@"
}
has_ca_certificates() { [ "$has_ca" = 1 ]; }
mv() {
    if [ "${1:-}" = --version ]; then [ "$has_gnu_mv" = 1 ]; return; fi
    builtin command mv "$@"
}
missing_tool='' has_ca=1 has_gnu_mv=1
[ -z "$(missing_dependency_packages)" ]
missing_tool=curl
[ "$(missing_dependency_packages)" = curl ]
missing_tool=wget
[ -z "$(missing_dependency_packages)" ]
missing_tool=realpath
[ "$(missing_dependency_packages)" = coreutils ]
missing_tool='' has_gnu_mv=0
[ "$(missing_dependency_packages)" = coreutils ]
missing_tool=curl has_ca=0 has_gnu_mv=0
[ "$(missing_dependency_packages)" = $'curl\nca-certificates\ncoreutils' ]

calls=0 install_fails=0 stays_missing=0
run_with_retry() {
    shift 2
    calls=$((calls + 1))
    case "$*" in
        'apt-get update -qq') return 0 ;;
        'apt-get install -y -qq curl'|'dnf install -y -q curl'|'yum install -y -q curl'|'apk add --no-cache curl') ;;
        *) printf '非预期的依赖安装：%s\n' "$*" >&2; return 99 ;;
    esac
    [ "$install_fails" = 0 ] || return 1
    [ "$stays_missing" = 1 ] || missing_tool=''
}
missing_tool='' has_ca=1 has_gnu_mv=1 OS=debian
install_dependencies
[ "$calls" = 0 ]
for OS in debian ubuntu fedora centos alpine; do
    for missing_manager in '' dnf; do
        calls=0
        # command -v dnf 的模拟与 curl 缺失检测独立。
        command() {
            if [ "${1:-}" = -v ] && { [ "${2:-}" = "$missing_tool" ] || [ "${2:-}" = "$missing_manager" ]; }; then return 1; fi
            if [ "${1:-}" = -v ] && [ "${2:-}" = dnf ]; then return 0; fi
            builtin command "$@"
        }
        missing_tool=curl
        install_dependencies
        [ -z "$missing_tool" ]
        if [[ "$OS" = debian || "$OS" = ubuntu ]]; then [ "$calls" = 2 ]; else [ "$calls" = 1 ]; fi
    done
done
OS=debian missing_tool=curl install_fails=1 calls=0
if install_dependencies; then exit 1; fi
[ "$calls" = 2 ] && [ "$missing_tool" = curl ]
install_fails=0 stays_missing=1 calls=0
if install_dependencies; then exit 1; fi
[ "$calls" = 2 ]
OS=unsupported calls=0
if install_dependencies; then exit 1; fi
[ "$calls" = 0 ]

echo '安装器依赖检查测试通过'
