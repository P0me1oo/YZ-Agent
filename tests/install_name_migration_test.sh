#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ "${1:-}" = --case ]; then
    case_root="$2"
    requested_scenario="$3"
    scenario="${requested_scenario#root-}"
    root_case=0
    [[ "$requested_scenario" != root-* ]] || root_case=1
    manager="$4"
    source "$case_root/library.sh"
    INSTALL_ROOT="$case_root/etc"
    BACKUP_DIR="$INSTALL_ROOT/backups"
    CONFIG_FILE="$INSTALL_ROOT/config.yml"
    CREDENTIALS_FILE="$INSTALL_ROOT/credentials.env"
    INSTALL_META="$INSTALL_ROOT/install-meta.json"
    BIN_DIR_FILE="$INSTALL_ROOT/bin-dir"
    INSTALLER_COPY_PATH="$INSTALL_ROOT/install.sh"
    DEFAULT_BIN_DIR="$case_root/bin"
    CLI_SYMLINK_PATH="$case_root/bin/xbctl"
    SYSTEMD_SERVICE_PATH="$case_root/services/yz-agent.service"
    OPENRC_SERVICE_PATH="$case_root/services/yz-agent"
    OPENRC_LOG_PATH="$case_root/logs/yz-agent.log"
    SERVICE_MANAGER="$manager"
    SERVICE_PATH="$OPENRC_SERVICE_PATH"
    [ "$manager" != systemd ] || SERVICE_PATH="$SYSTEMD_SERVICE_PATH"
    ACTION=upgrade
    ARCH=amd64
    RELEASE_VERSION=v1.14.0
    HEALTH_ENABLED=0
    old=xboard-node
    new=yz-agent
    if [ "$scenario" = downgrade ]; then old=yz-agent; new=xboard-node; fi
    if [ "$scenario" = repeat ]; then old=yz-agent; fi
    if [ "$scenario" = interim ]; then old=agent; fi
    case "$scenario" in modern-move*) old=yz-agent; BIN_DIR="$case_root/newbin" ;; esac
    if [ "$root_case" -eq 1 ]; then
        CURRENT_INSTALL_ROOT="$case_root/yz-agent"
        LEGACY_INSTALL_ROOT="$case_root/xboard-node"
        if [ "$old" = yz-agent ]; then set_install_root "$CURRENT_INSTALL_ROOT";
        else set_install_root "$LEGACY_INSTALL_ROOT"; fi
    fi
    mkdir -p "$INSTALL_ROOT" "$DEFAULT_BIN_DIR" "$case_root/services" "$case_root/logs" "$case_root/package"
    printf 'health_port: 0\n' >"$CONFIG_FILE"
    printf 'TEST_INSTALLATION=1\n' >"$CREDENTIALS_FILE"
    printf 'original-meta\n' >"$INSTALL_META"
    if [ "$root_case" -eq 1 ]; then
        printf 'kernel:\n  config_dir: %s/instances/fixture\n' "$INSTALL_ROOT" >>"$CONFIG_FILE"
        mkdir -p "$INSTALL_ROOT/instances/fixture" "$INSTALL_ROOT/certs" "$INSTALL_ROOT/firewall"
        printf 'retained-counter\n' >"$INSTALL_ROOT/instances/fixture/usage"
        printf 'retained-certificate-fixture\n' >"$INSTALL_ROOT/certs/fixture.pem"
        printf '{"version":1,"scope":"fixture"}\n' >"$INSTALL_ROOT/firewall/fixture.json"
        stat -c '%d:%i' "$INSTALL_ROOT/instances/fixture/usage" >"$case_root/data-identity"
        if [ "$scenario" = conflict ]; then mkdir "$CURRENT_INSTALL_ROOT"; printf 'unrelated\n' >"$CURRENT_INSTALL_ROOT/keep"; fi
        if [ "$scenario" = binary-overlap ]; then BIN_DIR="$INSTALL_ROOT/bin"; fi
    fi
    printf '#!/usr/bin/env bash\nprintf "%s old\\n"\n' "$old" >"$DEFAULT_BIN_DIR/$old"
    printf '#!/usr/bin/env bash\nprintf "%s v1.14.0\\n"\n' "$new" >"$case_root/package/node"
    cat >"$case_root/package/xbctl" <<'CLI'
#!/usr/bin/env bash
case "$1 $2" in
    'config health-port') echo 0 ;;
    'config refresh-meta') : ;;
    'config migrate-root')
        [ "${YZ_TEST_CONFIG_MIGRATION_FAIL:-}" != 1 ] || exit 1
        shift 2
        while [ $# -gt 0 ]; do
            case "$1" in --config) source="$2" ;; --output) output="$2" ;; --from) previous="$2" ;; --to) target="$2" ;; esac
            shift 2
        done
        sed "s|$previous/|$target/|g" "$source" >"$output"
        ;;
    *) echo 'xbctl v1.14.0' ;;
esac
CLI
    cp "$case_root/package/xbctl" "$DEFAULT_BIN_DIR/xbctl"
    if [ "$new" = yz-agent ]; then
        cp "$case_root/package/xbctl" "$case_root/package/node"
        sed -i '/^case /i if [ "${1:-}" = -v ]; then echo "yz-agent v1.14.0"; exit 0; fi' "$case_root/package/node"
    fi
    chmod 755 "$case_root/package/"* "$DEFAULT_BIN_DIR/"*
    BINARY_SOURCE="$case_root/package/node"
    CLI_BINARY_SOURCE="$case_root/package/xbctl"
    old_service="$case_root/services/$old"
    new_service="$case_root/services/$new"
    if [ "$manager" = systemd ]; then old_service+=.service; new_service+=.service; fi
    printf 'original-service\n' >"$old_service"
    touch "$case_root/active-$old" "$case_root/enabled-$old"
    case "$scenario" in disabled-upgrade|disabled-failure) rm "$case_root/enabled-$old" ;; esac
    if [ "$scenario" = collision ]; then printf 'unrelated\n' >"$new_service"; fi
    if [ "$scenario" = binary-collision ]; then printf 'unrelated\n' >"$DEFAULT_BIN_DIR/$new"; fi
    case "$scenario" in command-entry*)
        mkdir "$case_root/entry"
        CLI_SYMLINK_PATH="$case_root/entry/xbctl"
        ln -s "$DEFAULT_BIN_DIR/xbctl" "$CLI_SYMLINK_PATH"
        if [ "$scenario" = command-entry-collision ]; then printf 'unrelated\n' >"$case_root/entry/yz-agent"; fi
        ;;
    esac

    # 模拟服务管理器，验证切换顺序，不接触系统服务。
    mock_service() {
        local action="$1" name="${2%.service}"
        case "$action" in
            stop) rm -f "$case_root/active-$name" ;;
            restart|start)
                if [ "$name" = "$new" ] && [ "$old" != "$new" ]; then
                    [ ! -f "$case_root/active-$old" ]
                    [ -x "$DEFAULT_BIN_DIR/$new" ]
                    [ -f "$new_service" ]
                    case "$scenario" in
                        restart-failure) return 1 ;;
                        interrupted) kill -TERM "$$" ;;
                    esac
                fi
                touch "$case_root/active-$name"
                ;;
            enable) touch "$case_root/enabled-$name" ;;
            is-enabled) [ -f "$case_root/enabled-$name" ] ;;
            disable)
                if [ "$scenario" = disable-failure ] && [ "$name" = "$old" ]; then return 1; fi
                rm -f "$case_root/enabled-$name"
                ;;
            status|is-active) [ -f "$case_root/active-$name" ] ;;
            *) : ;;
        esac
    }
    systemctl() { mock_service "$1" "${2:-}"; }
    rc-service() {
        [ "${1:-}" != --quiet ] || shift
        mock_service "$2" "$1"
    }
    rc-update() {
        case "$1" in
            add) mock_service enable "$2" ;;
            del) mock_service disable "$2" ;;
            show) [ ! -f "$case_root/enabled-$old" ] || echo "$old | default" ;;
        esac
    }
    wait_for_health() {
        if { [ "$scenario" = health-failure ] || [ "$scenario" = disabled-failure ] || [ "$scenario" = command-entry-failure ]; } && [ "$SERVICE_NAME" = "$new" ]; then return 1; fi
        service_is_active
    }
    show_recent_logs() { :; }
    if [ "$scenario" = config-failure ]; then export YZ_TEST_CONFIG_MIGRATION_FAIL=1; fi
    mv() {
        if [ "${1:-}" = -T ] && [ "${2:-}" = "$LEGACY_INSTALL_ROOT" ]; then
            [ "$scenario" != move-failure ] || return 1
            command mv "$@"
            if [ "$scenario" = move-interrupted ]; then kill -TERM "$$"; fi
            return
        fi
        command mv "$@"
    }
    ln() {
        if [ "$scenario" = alias-failure ] && [ "${1:-}" = -s ] && [ "${3:-}" = "$LEGACY_INSTALL_ROOT" ]; then return 1; fi
        command ln "$@"
    }
    if [ "$root_case" -eq 1 ]; then select_install_root; fi
    if [ "$scenario" = parent-lock ]; then
        mkdir "$INSTALL_ROOT/.install-lock"
        export YZ_INSTALL_PARENT_LOCK=1
    fi
    lock_installation
    load_install_paths
    if [ "$old" != xboard-node ]; then
        # 已合并的安装中，管理命令与节点共用同一个程序。
        cp "$case_root/package/xbctl" "$DEFAULT_BIN_DIR/$old"
        sed -i "/^case /i if [ \"\${1:-}\" = -v ]; then echo \"$old old\"; exit 0; fi" "$DEFAULT_BIN_DIR/$old"
        rm "$DEFAULT_BIN_DIR/xbctl"
    fi
    perform_upgrade
    exit 0
fi

TEST_ROOT=$(mktemp -d)
cleanup_name_test() {
    local exit_code=$?
    if [ "$exit_code" -ne 0 ]; then
        printf '名称迁移测试失败：%s/%s，行 %s：%s\n' "${manager:-unknown}" "${requested_scenario:-unknown}" "${failure_line:-unknown}" "${failure_command:-unknown}" >&2
        if [ -n "${case_root:-}" ] && [ -f "$case_root/result.log" ]; then
            cat "$case_root/result.log" >&2
        fi
    fi
    rm -rf "$TEST_ROOT"
}
trap cleanup_name_test EXIT
trap 'failure_line="$LINENO"; failure_command="$BASH_COMMAND"' ERR
scenario_count=0
for manager in systemd openrc; do
    for requested_scenario in upgrade downgrade repeat parent-lock modern-move disabled-upgrade disabled-failure restart-failure health-failure disable-failure interrupted collision binary-collision command-entry command-entry-failure command-entry-collision interim root-upgrade root-downgrade root-repeat root-parent-lock root-restart-failure root-health-failure root-disable-failure root-interrupted root-move-failure root-move-interrupted root-alias-failure root-config-failure root-conflict root-interim root-disabled-upgrade root-disabled-failure root-binary-overlap; do
        scenario="${requested_scenario#root-}"
        scenario_count=$((scenario_count + 1))
        case_root="$TEST_ROOT/$manager-$requested_scenario"
        mkdir -p "$case_root"
        sed '/^main "$@"$/d' "$REPO_ROOT/install.sh" >"$case_root/library.sh"
        result=0
        bash "$0" --case "$case_root" "$requested_scenario" "$manager" >"$case_root/result.log" 2>&1 || result=$?
        expected=0
        case "$scenario" in *failure|*collision|conflict|binary-overlap) expected=1 ;; interrupted|move-interrupted) expected=143 ;; esac
        if [ "$result" -ne "$expected" ]; then cat "$case_root/result.log"; exit 1; fi
        old=xboard-node; new=yz-agent
        [ "$scenario" != downgrade ] || { old=yz-agent; new=xboard-node; }
        [ "$scenario" != repeat ] || old=yz-agent
        [ "$scenario" != interim ] || old=agent
        installed_bin="$case_root/bin"
        if [ "$scenario" = modern-move ]; then old=yz-agent; installed_bin="$case_root/newbin"; fi
        old_service="$case_root/services/$old"; new_service="$case_root/services/$new"
        if [ "$manager" = systemd ]; then old_service+=.service; new_service+=.service; fi
        if [ "$expected" = 0 ]; then
            [ -x "$installed_bin/$new" ]
            [ -f "$new_service" ]
            [ -f "$case_root/active-$new" ]
            if [ "$scenario" = disabled-upgrade ]; then [ ! -f "$case_root/enabled-$new" ];
            else [ -f "$case_root/enabled-$new" ]; fi
            if [ "$old" != "$new" ]; then
                [ ! -e "$case_root/bin/$old" ]
                [ ! -e "$old_service" ]
                [ ! -e "$case_root/active-$old" ]
                [ ! -e "$case_root/enabled-$old" ]
            fi
        else
            [ "$(cat "$old_service")" = original-service ]
            [ -x "$case_root/bin/$old" ]
            [ -f "$case_root/active-$old" ]
            if [ "$scenario" = disabled-failure ]; then [ ! -f "$case_root/enabled-$old" ];
            else [ -f "$case_root/enabled-$old" ]; fi
            if [ "$scenario" = collision ]; then [ "$(cat "$new_service")" = unrelated ];
            else [ ! -e "$new_service" ]; fi
            if [ "$scenario" = binary-collision ]; then [ "$(cat "$case_root/bin/$new")" = unrelated ];
            else [ ! -e "$case_root/bin/$new" ]; fi
        fi
        config_root="$case_root/etc"
        if [[ "$requested_scenario" == root-* ]]; then
            active_name="$old"
            [ "$expected" != 0 ] || active_name="$new"
            config_root="$case_root/yz-agent"
            [ "$active_name" = yz-agent ] || config_root="$case_root/xboard-node"
            grep -Fq "config_dir: $config_root/instances/fixture" "$config_root/config.yml"
            [ "$(cat "$config_root/instances/fixture/usage")" = retained-counter ]
            [ "$(stat -c '%d:%i' "$config_root/instances/fixture/usage")" = "$(cat "$case_root/data-identity")" ]
            [ "$(cat "$config_root/certs/fixture.pem")" = retained-certificate-fixture ]
            [ "$(cat "$config_root/firewall/fixture.json")" = '{"version":1,"scope":"fixture"}' ]
            if [ "$expected" = 0 ]; then grep -Fq "$config_root/config.yml" "$new_service"; fi
            other_root="$case_root/xboard-node"
            [ "$config_root" != "$other_root" ] || other_root="$case_root/yz-agent"
            if [ "$scenario" = conflict ]; then [ "$(cat "$other_root/keep")" = unrelated ];
            else [ ! -e "$other_root" ] && [ ! -L "$other_root" ]; fi
        else
            [ "$(cat "$config_root/config.yml")" = 'health_port: 0' ]
        fi
        [ "$(cat "$config_root/credentials.env")" = 'TEST_INSTALLATION=1' ]
        case "$scenario" in
            command-entry)
                [ "$(readlink "$case_root/entry/yz-agent")" = "$case_root/bin/yz-agent" ]
                [ ! -e "$case_root/entry/xbctl" ]
                [ ! -e "$case_root/bin/xbctl" ]
                ;;
            command-entry-failure|command-entry-collision)
                [ "$(readlink "$case_root/entry/xbctl")" = "$case_root/bin/xbctl" ]
                if [ "$scenario" = command-entry-collision ]; then [ "$(cat "$case_root/entry/yz-agent")" = unrelated ];
                else [ ! -e "$case_root/entry/yz-agent" ]; fi
                ;;
            modern-move)
                [ "$(readlink "$case_root/bin/yz-agent")" = "$case_root/newbin/yz-agent" ]
                ;;
        esac
        if [ "$scenario" = parent-lock ]; then
            [ -d "$config_root/.install-lock" ]
            [ "$case_root/bin/$new" -ef "$case_root/package/node" ]
            [ ! -e "$case_root/bin/xbctl" ]
        else
            [ ! -e "$config_root/.install-lock" ]
        fi
    done
done

# 回退清理失败时保留新安装和备份；未启动过新服务则可直接恢复旧安装。
for started in 0 1; do
    (
        case_root="$TEST_ROOT/rollback-cleanup-$started"
        mkdir -p "$case_root/root" "$case_root/backup" "$case_root/bin"
        source <(sed '/^main "$@"$/d' "$REPO_ROOT/install.sh")
        INSTALL_ROOT="$case_root/root"
        BACKUP_PATH="$case_root/backup"
        SERVICE_PATH="$case_root/new.service"
        PREVIOUS_SERVICE_PATH="$case_root/old.service"
        REPLACEMENT_SERVICE_STARTED="$started"
        printf 'new-program\n' >"$case_root/bin/yz-agent"
        printf 'old-program\n' >"$case_root/bin/xboard-node"
        printf 'new-service\n' >"$SERVICE_PATH"
        service_stop() { :; }
        service_is_active() { return 1; }
        cleanup_installation_firewall() { return 1; }
        # 此处只验证失败前置条件；其余恢复步骤由上面的文件迁移场景覆盖。
        if [ "$started" = 0 ]; then
            cleanup_installation_firewall() { echo '不应清理尚未启动的新程序' >&2; exit 1; }
            BACKUP_PATH=""
            PREVIOUS_CLI_PATH=""
            SERVICE_PATH="$case_root/missing.service"
            load_health_port_from_config() { :; }
            service_reload() { :; }
            service_disable() { :; }
            rollback_install >/dev/null
        else
            if rollback_install >/dev/null; then echo '清理失败不能继续回退' >&2; exit 1; fi
            [ "$ROLLBACK_FAILED" = 1 ]
            [ "$(cat "$SERVICE_PATH")" = new-service ]
        fi
        [ "$(cat "$case_root/bin/yz-agent")" = new-program ]
        [ "$(cat "$case_root/bin/xboard-node")" = old-program ]
    )
done
echo "installer name migration tests passed ($scenario_count scenarios)"
echo 'installer rollback firewall cleanup checks passed (2 scenarios)'
