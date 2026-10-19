#!/bin/sh
# 在专用 rootfs 和独立网络中执行原生命令或完整 Node 验证。
set -eu

root=$(realpath "${1:?需要专用 rootfs 路径}")
backend=${2:?需要 ufw 或 firewalld}
redirect=${3:?需要 nftables 或 iptables}
mode=${4:-backend}
kernel=${5:-xray}
case "$backend" in ufw|firewalld) ;; *) exit 2 ;; esac
case "$redirect" in nftables|iptables) ;; *) exit 2 ;; esac
case "$mode" in backend|node|source|source-node) ;; *) exit 2 ;; esac
case "$kernel" in xray|singbox) ;; *) exit 2 ;; esac
if [ "$root" = / ] || [ ! -f "$root/.yz-firewall-test-rootfs" ]; then
    echo '拒绝操作：需要带 .yz-firewall-test-rootfs 标记的专用 rootfs' >&2
    exit 2
fi

exec unshare --mount --net --pid --fork /bin/sh -s -- "$root" "$backend" "$redirect" "$mode" "$kernel" <<'OUTER'
set -eu
root=$1
mount --make-rprivate /
mount --bind "$root" "$root"
mkdir -p "$root/proc" "$root/sys" "$root/run" "$root/dev"
mount -t proc proc "$root/proc"
mount -t sysfs sysfs "$root/sys"
mount -t tmpfs tmpfs "$root/run"
mount -t tmpfs tmpfs "$root/tmp"
mount -t tmpfs tmpfs "$root/dev"
for device in null zero random urandom; do
    case "$device" in null) minor=3 ;; zero) minor=5 ;; random) minor=8 ;; urandom) minor=9 ;; esac
    mknod -m 666 "$root/dev/$device" c 1 "$minor"
done
ln -s /proc/self/fd "$root/dev/fd"
ln -s /proc/self/fd/0 "$root/dev/stdin"
ln -s /proc/self/fd/1 "$root/dev/stdout"
ln -s /proc/self/fd/2 "$root/dev/stderr"
exec chroot "$root" /bin/sh -s -- "$2" "$3" "$4" "$5" <<'GUEST'
set -eu
export LC_ALL=C LANG=C
export YZ_FIREWALL_NATIVE=1 YZ_FIREWALL_BACKEND=$1 YZ_FIREWALL_REDIRECT=$2
printf 'isolated-rootfs\n' > /run/yz-firewall-test
ip link set lo up
ip netns add yzfw-client
ip link add yzfw0 type veth peer name yzfw1
ip link set yzfw1 netns yzfw-client
ip addr add 192.0.2.1/24 dev yzfw0
ip -6 addr add 2001:db8:1::1/64 dev yzfw0 nodad
ip link set yzfw0 up
ip netns exec yzfw-client ip link set lo up
ip netns exec yzfw-client ip addr add 192.0.2.2/24 dev yzfw1
ip netns exec yzfw-client ip -6 addr add 2001:db8:1::2/64 dev yzfw1 nodad
ip netns exec yzfw-client ip link set yzfw1 up

if [ "$1" = ufw ]; then
    ufw --force reset >/dev/null
    ufw default deny incoming >/dev/null
    ufw --force enable >/dev/null
else
    mkdir -p /run/dbus
    dbus-daemon --system --fork --nopidfile
    firewalld --nofork --nopid >/run/firewalld-test.log 2>&1 &
    count=0
    until firewall-cmd --state >/dev/null 2>&1; do
        count=$((count + 1))
        if [ "$count" -gt 40 ]; then
            cat /run/firewalld-test.log
            exit 1
        fi
        sleep 0.25
    done
    firewall-cmd --zone=public --change-interface=yzfw0 >/dev/null
fi

if [ "$3" = backend ]; then
    exec /work/firewall-linux.test -test.run='^TestNativeFirewallLifecycle$' -test.v -test.timeout=8m
fi
if [ "$3" = source ]; then
    exec /work/firewall-linux.test -test.run='^TestNativeSourceFirewallLifecycle$' -test.v -test.timeout=8m
fi
if [ "$3" = source-node ]; then
    exec /work/service-linux.test -test.run='^TestNativeSourceSOCKSRuntime$' -test.v -test.timeout=8m
fi
exec python3 /work/test-firewall-node.py --kernel "$4" --backend "$1" --redirect "$2"
GUEST
OUTER
