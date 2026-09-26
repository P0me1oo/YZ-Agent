package xray

import (
	"net/netip"

	"github.com/xtls/xray-core/transport/internet"
)

// SetTrustedProxyPrefixes 把前置服务器名单交给 Xray 内核（进程内所有 Xray 实例共用）。
//
// 名单内的来源可以在连接开头附带 PROXY 头说明真实用户地址，内核据此确定来源，
// 设备数随之按真实地址计算；不带头的连接照常处理。名单外的来源不解析 PROXY 头，
// 无法冒充来源地址，直连用户不受影响。名单为空时不信任任何来源，旧开关不扩大信任范围。
// 已建立的监听在下一次接收连接时使用新名单，无需重载内核。
func SetTrustedProxyPrefixes(prefixes []netip.Prefix) {
	internet.SetProxyProtocolTrustedPrefixes(prefixes)
}
