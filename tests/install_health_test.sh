#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source <(sed '/^main "$@"$/d' "$REPO_ROOT/install.sh")
trap - EXIT ERR INT TERM

# 模拟时间、HTTP 和服务状态，不访问网络或操作系统服务。
log_info() { :; }
log_error() { :; }
sleep() { SECONDS=$((SECONDS + $1)); }
service_is_active() {
    service_checks=$((service_checks + 1))
    [ "$service_checks" -le "$active_checks" ]
}
curl() {
    calls=$((calls + 1))
    local bypass=0 connect_timeout=0 max_time=0 url=''
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --noproxy) [ "$2" != '*' ] || bypass=1; shift 2 ;;
            --connect-timeout) connect_timeout="$2"; shift 2 ;;
            --max-time) max_time="$2"; shift 2 ;;
            -fsS) shift ;;
            *) url="$1"; shift ;;
        esac
    done
    if [ "$bypass" -ne 1 ] || [ "$connect_timeout" -le 0 ] || [ "$max_time" -gt 2 ] || \
        [ "$max_time" -le 0 ] || [ "$max_time" -gt "$((HEALTH_WAIT_SECONDS - SECONDS))" ] || \
        [ "$url" != "http://127.0.0.1:${HEALTH_PORT}/healthz" ]; then
        bad_request=1
        return 2
    fi
    last_max_time="$max_time"
    if [ "$simulate_timeout" -eq 1 ]; then
        SECONDS=$((SECONDS + max_time))
        return 28
    fi
    [ "$calls" -ge "$ready_after" ] || return 22
}
reset_case() {
    SECONDS=0
    HEALTH_ENABLED=1
    HEALTH_WAIT_SECONDS=180
    calls=0 service_checks=0 bad_request=0 last_max_time=0
    ready_after=1 active_checks=1000 simulate_timeout=0
}

reset_case
wait_for_health
wait_for_health
[ "$calls" -eq 2 ] && [ "$bad_request" -eq 0 ]

# 覆盖旧 30 次重试窗口之外完成启动的场景。
reset_case
ready_after=45
wait_for_health
[ "$calls" -eq 45 ] && [ "$bad_request" -eq 0 ]

# 始终未就绪不能算成功，达到总时限后仍交给安装事务回滚。
reset_case
ready_after=1000
if wait_for_health; then exit 1; fi
[ "$SECONDS" -ge 180 ] && [ "$calls" -gt 30 ] && [ "$bad_request" -eq 0 ]

# 请求自身耗时也计入总时限，最后一次请求不越过剩余预算。
reset_case
HEALTH_WAIT_SECONDS=4 simulate_timeout=1
if wait_for_health; then exit 1; fi
[ "$SECONDS" -eq 4 ] && [ "$calls" -eq 2 ] && [ "$last_max_time" -eq 1 ] && [ "$bad_request" -eq 0 ]

reset_case
ready_after=1000 active_checks=3
if wait_for_health; then exit 1; fi
[ "$calls" -eq 2 ]

reset_case
active_checks=0
if wait_for_health; then exit 1; fi
[ "$calls" -eq 0 ]

reset_case
HEALTH_ENABLED=0
wait_for_health
[ "$calls" -eq 0 ]

echo '安装器健康检查测试通过'
