# YZ-Agent v2.42.0 发布

维护者：P0me1oo。日期：2026-11-20。

[v2.42.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.42.0) 已正式发布并设为 Latest，固定源码 `01a591230317864929a3c8e78dcfad76656f8efc`。本版汇总双栈监听检查修复，以及 Xray 的 SS2022 v2ray-plugin WebSocket／TLS 入站适配。

IPv4 配置对应 IPv6 双栈监听时，按实际套接字的 IPv4 接受设置判断；仅 IPv6 监听不会误判为 IPv4 可用。保留进程归属、线程网络空间及关闭检查，并在复制描述符后核对地址、端口、类型与监听状态。检查不建立连接，具体机制见[健康检查](healthcheck-logging.md#v2420-双栈监听识别修复)。

插件入站要求 Xray、`v2ray-plugin`、WebSocket 和 `mode=websocket;mux=0`，路径、Host、TLS 由面板对应字段下发。客户端须关闭插件复用，插件链路仅保证 TCP；sing-box 插件入站及其它不支持组合明确拒绝。实连用例覆盖 SS2022 AES-128／256、WebSocket／TLS 四种组合、用户撤销拒绝与恢复后重新可用。

Linux amd64 / arm64 安装包使用 Go 1.27.4、正式功能标签、`CGO_ENABLED=0` 和固定干净提交构建。核心保持 YZ-sing-box v1.16.0 与 YZ-Xray-core v26.12.0，没有改变模块依赖或使用相邻核心工作区。

Go 1.27.4 / Linux amd64 / Zig 0.15.2、正式功能标签下，41 包完整竞态回归的 1087 个顶层用例通过；8 个固定依赖包的 43 项、2 项独立 Mihomo 客户端检查和 10 组安装脚本检查通过，无失败或数据竞争。6 项既有原生防火墙、隔离来源或端口选择用例按条件跳过，不计为通过。命令与清单见[测试核验](release-v2.42.0-tests.json)。健康检查、模型、sing-box 和服务层静态检查通过；额外检查 Xray 时报告 `internal/kernel/xray/dispatcher.go:267` 的既有上下文取消告警，该文件未在本版修改，不将其记作通过。

13 份正式附件已匿名下载，文件大小、本机 SHA-256、GitHub 摘要和校验清单一致；两架构构建来源、操作系统、版本、核心依赖与 `vcs.modified=false` 已核对，历史兼容程序与 Agent 逐字节相同，见[附件核验](release-v2.42.0-verification.json)。

GitHub Actions 两次自动运行均因账户账单锁定未启动，测试步骤数为 0；发布采用上述本机验证与干净固定源码构建，镜像任务跳过。没有修改测试门禁或将未运行的远端检查记为通过。

本次没有升级运行节点，也未发布 Docker 镜像。通过面板“服务器管理”的单台或批量“升级”入口更新，以操作结果及节点回报 v2.42.0 确认完成。回退版本为 v2.41.0；启用新插件配置后回退，须先恢复旧版支持的节点配置。
