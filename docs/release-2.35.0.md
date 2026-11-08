# YZ-Agent v2.35.0 发布记录

日期：2026-11-08。维护者：P0me1oo。

[v2.35.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.35.0) 已正式发布并标记为 Latest。固定源码 `e5249417c0a4c8b3eb56162f3fcc825c3482462c`，包含 Lite 通用主机监控停采/恢复以及此前 v2.34.0 未单独发布的每个 IP 连接数功能。发布后的记录提交不改变版本标签和安装包来源。

## 行为

服务器绑定并启用 Lite 后，停止机器及其节点的 CPU、系统负载、内存、交换区、磁盘和通用网速采样与上报，面板同步停止对应历史写入。解绑或关闭 Lite 自动恢复，Lite 暂时不可达不恢复重复采集。代理流量、用户/连接/IP/设备状态、限速事件、核心运行及控制心跳保持。

Xray、sing-box 和普通 WireGuard 按完整公网来源 IP 上报活跃连接数，IPv6 不合并；Xray 统计包含热重载后仍在排空的旧连接。配套面板 2.9.0、管理端 0.93.0；Solid-US LA 面板已更新，本次没有升级运行中的 Agent。

两个核心固定来源不变：YZ-sing-box v1.15.1、YZ-Xray-core v0.0.0-20261103192507-3c480b0c0d73。

## 验证

复用同一程序源码先前已通过的监控、面板通信、控制、机器、节点服务、流量跟踪与流量保护 7 包竞态检查；面板对应双数据库各 109 项、1091 条断言通过。程序源码在验证、提交和构建之间未修改。

发布前补充 Go 1.27.3、Linux amd64、GCC 14 的以下检查，均使用正式构建标签 `with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api`：

- `go test -mod=readonly -race -count=1 -timeout 5m -json -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./internal/kernel/xray ./internal/directwg`：144 个顶层用例通过，1 项因系统分配端口不在防盗用测试范围内跳过，无数据竞争。
- `go test -mod=readonly -race -count=1 -timeout 5m -json -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' -run 'IPConnection|Connection|ConnTracker|Device|ConnLimit|RelaySource|RelayUserAlive|Snapshot' ./internal/kernel/singbox`：23 个顶层用例通过，无数据竞争。
- 升级版本、下载镜像、服务管理、健康检查、核心默认值、隐私、安装路径、名称迁移共 8 组脚本检查及 shell 语法通过。

GitHub Actions [运行 42453194679](https://github.com/P0me1oo/YZ-Agent/actions/runs/42453194679) 因账号账单锁定未启动，所有测试任务步骤数均为 0；本次采用本地验证和构建，没有将启动失败表述为代码测试失败或 CI 通过。未新增完整 CI、外部 Mihomo、四组原生防火墙或生产节点联调结果。

## 安装包

从干净的固定源码构建 Linux amd64 和 arm64，构建参数采用 `-mod=readonly -trimpath -buildvcs=true`，链接信息包含 v2.35.0、构建时间与完整提交，`CGO_ENABLED=0`。四份构建信息的 VCS revision、操作系统、架构和 `vcs.modified=false` 均匹配；amd64 的 Agent 与 xbctl 实际运行版本输出一致。

共 12 份附件：两个架构的 Agent、兼容名称 xboard-node、xbctl，安装脚本、四份构建信息及 SHA256SUMS。所有附件均已匿名下载，与本地文件和 GitHub 附件摘要一致，11 项 SHA256SUMS 全部通过；兼容名称与对应架构主程序字节相同。

| 主程序 | SHA-256 |
| --- | --- |
| yz-agent-linux-amd64 | `15f5ca1d9b734d6895012980241818d444f48b1c4b0719841095c9ce0f85aeee` |
| yz-agent-linux-arm64 | `13308a7d31fac718690e6d3edcb06af9c63a859e0083e165e5bb29590e78f4fd` |

完整附件与来源核验见[结构化记录](release-2.35.0-verification.json)。构建没有包含工作区规则或真实凭据，没有发布 Docker 镜像。临时编译工具、测试文件、构建及验证下载已清理，正式安装包保留在 Release。

## 更新与回退

在面板“服务器管理”选择服务器并执行“升级”，以机器回报版本 v2.35.0 确认完成。升级后绑定并启用 Lite，即可停止重复主机采集；仅发布安装包不会自动升级已有节点。

最近正式回退版本为 v2.33.1；回退后 Agent 恢复旧采样行为，新面板仍可在绑定启用期间忽略主机负载。配置、状态文件和流量批次格式保持兼容，不需要删除数据或更换核心依赖。
