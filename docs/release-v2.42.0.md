# YZ-Agent v2.42.0 发布

维护者：P0me1oo。日期：2026-11-20。

本版汇总双栈监听检查修复，以及 Xray 的 SS2022 v2ray-plugin WebSocket／TLS 入站适配。发布地址为 [v2.42.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.42.0)，固定源码与正式附件核验在发布完成后补记。

IPv4 配置对应 IPv6 双栈监听时，按实际套接字的 IPv4 接受设置判断；仅 IPv6 监听不会误判为 IPv4 可用。保留进程归属、线程网络空间及关闭检查，并在复制描述符后核对地址、端口、类型与监听状态。检查不建立连接，具体机制见[健康检查](healthcheck-logging.md#v2420-双栈监听识别修复)。

插件入站要求 Xray、`v2ray-plugin`、WebSocket 和 `mode=websocket;mux=0`，路径、Host、TLS 由面板对应字段下发。客户端须关闭插件复用，插件链路仅保证 TCP；sing-box 插件入站及其它不支持组合明确拒绝。实连用例覆盖 SS2022 AES-128／256、WebSocket／TLS 四种组合、用户撤销拒绝与恢复后重新可用。

Linux amd64 / arm64 安装包使用 Go 1.27.4、正式功能标签、`CGO_ENABLED=0` 和固定干净提交构建。核心保持 YZ-sing-box v1.16.0 与 YZ-Xray-core v26.12.0，没有改变模块依赖或使用相邻核心工作区。

专项竞态检查、安装脚本和 Mihomo WireGuard 客户端检查已通过，完整发布检查进行中。健康检查、模型、sing-box 和服务层静态检查通过；额外检查 Xray 时报告 `internal/kernel/xray/dispatcher.go:267` 的既有上下文取消告警，该文件未在本版修改，不将其记作通过。

本次发布不升级运行节点，也不发布 Docker 镜像。正式附件可获取后，通过面板“服务器管理”的单台或批量“升级”入口更新，以操作结果及节点回报 v2.42.0 确认完成。回退版本为 v2.41.0；启用新插件配置后回退，须先恢复旧版支持的节点配置。
