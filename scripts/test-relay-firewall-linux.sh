#!/usr/bin/env bash
# Run compiled firewall/service tests in a private rootfs and network namespace.
set -euo pipefail
work=$(realpath "${1:?compiled tests directory required}")
driver=${2:-nftables}
suite=${3:-firewall}
case "$driver" in nftables|iptables) ;; *) exit 2 ;; esac
case "$suite" in firewall|service|policy) ;; *) exit 2 ;; esac
binary=$suite
[[ "$suite" == policy ]] && binary=firewall
[[ -f "$work/$binary.test" && "$work" == /tmp/* ]]
root=$(mktemp -d "$work/rootfs.XXXXXX")
trap 'rmdir "$root" 2>/dev/null || true' EXIT
unshare --mount --net --pid --fork bash -s -- "$work" "$root" "$driver" "$suite" <<'OUTER'
set -euo pipefail
work=$1 root=$2 driver=$3 suite=$4
mount --make-rprivate /
mount -t tmpfs tmpfs "$root"
mkdir -p "$root"/{usr,etc,run,tmp,proc,sys,dev} "$root$work"
mount --bind /usr "$root/usr"
mount -o remount,bind,ro "$root/usr"
mkdir -p "$root/etc/alternatives"
mount --bind /etc/alternatives "$root/etc/alternatives"
mount -o remount,bind,ro "$root/etc/alternatives"
for entry in bin sbin lib lib64; do ln -s "usr/$entry" "$root/$entry"; done
mount --bind "$work" "$root$work"
mount -t proc proc "$root/proc"
mount -t sysfs sysfs "$root/sys"
for device in null zero random urandom; do
 case "$device" in null) minor=3 ;; zero) minor=5 ;; random) minor=8 ;; urandom) minor=9 ;; esac
 mknod -m 666 "$root/dev/$device" c 1 "$minor"
done
ln -s /proc/self/fd "$root/dev/fd"
ln -s /proc/self/fd/0 "$root/dev/stdin"
ln -s /proc/self/fd/1 "$root/dev/stdout"
ln -s /proc/self/fd/2 "$root/dev/stderr"
exec chroot "$root" /bin/bash -s -- "$work" "$driver" "$suite" <<'GUEST'
set -euo pipefail
work=$1
export YZ_FIREWALL_NATIVE=1 YZ_RELAY_DRIVER=$2
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
if [[ "$3" == service ]]; then
 ip addr add 8.8.8.1/24 dev yzfw0
 ip -6 addr add 2001:4860:4860::1/64 dev yzfw0 nodad
 ip netns exec yzfw-client ip addr add 8.8.8.8/24 dev yzfw1
 ip netns exec yzfw-client ip -6 addr add 2001:4860:4860::8888/64 dev yzfw1 nodad
 export TEST_RELAY_FIREWALL_EXPORT="$work/export"
 exec "$work/service.test" -test.run='^TestNativeRelayNextSnapshotsBothCores$' -test.v -test.timeout=3m
fi
if [[ "$3" == policy ]]; then
 exec "$work/firewall.test" -test.run='^TestNativeNextSourcePolicyLifecycle$' -test.v -test.timeout=3m
fi
exec "$work/firewall.test" -test.run='^TestNativeRelayFirewallDualStackAndRollback$' -test.v -test.timeout=3m
GUEST
OUTER
