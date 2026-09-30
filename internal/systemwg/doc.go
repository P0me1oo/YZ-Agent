// Package systemwg 将中转隧道中的 TCP 交给 Linux 网络栈。
// 每条链路持有独立、未命名的网络空间，重复隧道地址不会进入宿主机路由表。
// 隧道内使用 SOCKS 转交目标地址，两端必须一起升级。
package systemwg
