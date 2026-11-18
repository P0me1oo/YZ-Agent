//go:build linux

package main

import "runtime"

func init() {
	// 初始线程留在宿主网络空间，供 /proc/net 等依赖进程主线程的接口使用。
	runtime.LockOSThread()
}
