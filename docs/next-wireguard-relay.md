# Next WireGuard 中转适配

Agent 2.52.0 与 Next 0.28.0 / Dash 0.27.0 配套，当前为本地源码，未发布或部署。完整操作、限制和数据路径见 [Next WG 中转](../../YZboard-Next/YZboard-Next/docs/wireguard-relay.md)。

Linux 握手增加 `wireguard_relay: 1`；新 WG 配置携带 `transport: socks-v1`。该格式严格校验两端主机地址，不允许默认路由或额外 peers。非 Linux 明确拒绝，避免与旧核心直接 WG 格式混用；省略 transport 的旧面板继续兼容，普通 WG 不受影响。

系统 WG 使用现有固定依赖 `github.com/sagernet/wireguard-go`，工作并发为 `min(2, GOMAXPROCS)`；没有修改 go.mod 的核心版本。TUN 与外层套接字分属隧道、宿主网络空间，生命周期随核心管理。BBR 不可用时用 CUBIC，并在该空间打开 TCP MTU 黑洞探测。域名对端每 30 秒刷新，解析有 5 秒上限，关闭时取消并等待刷新任务退出。

Xray 的固定版本在传输层保存全局 DNS 引用。Agent 在 WG 落地的 freedom 出站边界注入实例内解析器：选路仍看域名，实际 TCP 连接和逐包 UDP 目标使用该实例解析，公网地址限制保持执行。不修改进程全局 DNS；删除条件是固定核心提供实例隔离的相同能力并通过真实链路回归。

固定 Xray HY2 接收器拒绝省略目标地址的后续 UDP 分片，而 sing-quic 采用这种格式。Agent 在既有配置构造注册点嵌入 UDP 适配，复用原认证、用户管理和 TCP 实现。每会话最多 64 个未完成报文、每报文最多 65507 字节、重组有效期 5 秒；支持乱序和重复片，拒绝目标冲突。原 worker 只识别具体 Server 类型，Agent 在传输监听注册点用本实例 validator 补充上下文。此适配依赖当前固定核心的注册表，升级时必须重新检查；上游解决后可移除。

sing-box 内部 SOCKS 入站与出站显式允许 UDP 分片，使超过隧道 MTU 的报文可在隧道内传递。它不保证公网外层分片可靠，也不是 UDP 重传机制。

测试入口：

```sh
go test -tags 'with_quic,with_wireguard,with_utls,with_gvisor' ./internal/systemwg ./internal/model ./internal/panel ./internal/kernel/xray ./internal/service ./internal/directwg ./internal/machine
TEST_WG_EXPORT=/tmp/本次任务目录 go test -tags 'with_quic,with_wireguard,with_utls' ./internal/kernel/singbox -run 'TestNextWireGuardSnapshotsRealChain|TestWireGuardRelayRuntime|TestWireGuardMultipleLandingsAndTraffic'
```

`TEST_WG_EXPORT` 目录由 Next 的 `TestWireGuardRelaySnapshotsAndLifecycle` 生成，含临时凭据，权限 0600，验证结束清理；不得提交或用于生产。真进程验收由 Next 的 `TestWireGuardAgentProcessRecovery` 读取 `TEST_AGENT_BINARY`，启动独立 Agent、面板监听及数据库 schema。
