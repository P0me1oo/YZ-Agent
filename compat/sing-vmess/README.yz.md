# Vision TLS 指针兼容补丁

源码来自 `SagerNet/sing-vmess v0.2.8-0.20250909125414-3aed155119a1`，完整提交为 `3aed155119a174c9290a4099841049f0cb275e02`。保留上游作者与 GPL-3.0-or-later 许可证，原始文件 SHA-256 见 `UPSTREAM.json`。

Node `v2.24.0` 正式流程的 Linux 完整竞态回归，在 `TestRealityShortIDRelayRuntime/xray/xtls-rprx-vision` 使用 sing-box 测试客户端时，触发 `vless/vision.go:87` 的 `checkptr` 崩溃。TLS 注册函数先将对象指针转换为 `uintptr`，稍后加上内部缓冲区偏移并恢复指针，丢失指针来源；检查上游当前实现后确认该写法仍存在。

补丁只修改 `vless/vision.go` 与 `vless/vision_utls.go`：注册函数直接传递 `unsafe.Pointer`，内部缓冲区定位使用 `unsafe.Add`，不再经过整数地址。协议、认证、TLS 配置和缓冲区处理逻辑保持原语义。新增检查覆盖标准 TLS、uTLS Conn 与浏览器指纹 UConn，并在垃圾回收后读取初始缓冲区。

根模块通过 `./compat/sing-vmess` 使用该副本，随 Node 固定提交发布；两个核心 fork 的固定依赖保持不变。CI 执行本模块的竞态测试，并保留双核心 REALITY 中转、普通 VLESS、Vision、TCP/UDP 和用户撤销恢复验收。没有关闭 `checkptr` 或竞态检查。

影响范围为 Node 内嵌 sing-box 使用该模块的 VLESS Vision 入站、出站及测试客户端。将来上游提供等价指针修复、接口兼容且上述检查通过后，可以删除该目录和根模块替换，改用固定上游版本。
