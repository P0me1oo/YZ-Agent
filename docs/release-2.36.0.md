# YZ-Agent v2.36.0 发布记录

日期：2026-11-08。维护者：P0me1oo。

[v2.36.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.36.0) 已正式发布并标记为 Latest。固定源码 `a9c48981ee93ddb830cb30e4b5ae42e3fb4aff82`，本次发布路由拦截事件采集和上报；源码记录提交不移动版本标签或改变安装包来源。没有升级运行中的节点或发布 Docker 镜像。

## 功能与配套

Xray、sing-box 在实际命中面板禁止访问规则时采集节点、用户、规则编号、目标、端口与 TCP/UDP，普通 WireGuard 保留真实用户；中转落地使用共享认证时保留未知用户，不从入口猜测归属。独立有界队列与固定批次落盘支持断网和重启重试，面板按批次去重。

支持面板 2.10.0 起的上报接口，推荐已部署的面板 2.11.0 / 管理端 0.95.0，在“路由管理 → 拦截记录”按用户查看。默认记录，保存天数在页面内设置；机器隐私保护继续优先。旧面板不启用采集，原访问记录接口保持兼容，两个核心依赖保持 YZ-sing-box v1.15.1、YZ-Xray-core v0.0.0-20261103192507-3c480b0c0d73。

新增、移除禁止访问规则或改变动作会改变出站集合，可能重建核心实例；同一规则的匹配条件修改沿用已有热更新路径。完整范围见[路由拦截记录](route-block-logs.md)。

## 验证

使用 Go 1.27.3、Linux amd64、临时解包 GCC 14，正式标签为 `with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api`。

- `go test -mod=readonly -race -count=1 -timeout=20m -json -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./...`：40 个有测试的包通过，978 个顶层用例通过，未发现数据竞争。覆盖普通连接、两个核心、配置重载、设备及流量状态、拦截记录、缓存与上报恢复。
- 普通回归中的 5 项原生防火墙运行/辅助项按隔离环境条件跳过，随后在专用 rootfs 与独立网络、挂载和进程空间完成四组合 12 项验证。另有 1 项既有随机端口检查因系统分配端口不在防盗用范围内跳过，未弱化或删除断言。
- Makefile 中列出的 8 个依赖兼容包（AnyTLS、VLESS、SS2022、Hysteria、sing-box 监听与 gRPC 等）竞态测试通过，共 43 个顶层用例，未发现数据竞争。
- 固定 Mihomo v1.19.31 下载包摘要核对通过，`TestWireGuardRealityClients` 与 `TestDirectWireGuardMihomo` 的双核心竞态联调均通过。
- 原生 UFW / firewalld × nftables / iptables 四组合，每组规则生命周期、来源过滤、Node SOCKS 真实双栈 TCP/UDP 三项均通过。环境是临时解包的 Debian 13 rootfs，非 CI 的 Ubuntu 24.04 镜像。开始时补齐未执行包安装脚本所缺的 shell、alternatives 和 UFW 模板配置后通过，没有修改主机防火墙或安装系统软件。
- 安装器版本判断、下载地址、服务管理、健康检查、核心默认值、隐私、目录及名称迁移共 8 组检查通过；shell 与 Python 语法检查通过。复用本次面板功能已通过的双数据库、前端和线上查询验证，未重复运行面板测试。

GitHub Actions [运行 42488421211](https://github.com/P0me1oo/YZ-Agent/actions/runs/42488421211) 因账号账单锁定未启动，测试和防火墙任务步骤数全部为 0。本版使用上述本机验证及固定源码构建，不将 GitHub Actions 状态描述为通过；没有生产节点升级或真实用户流量联调。

## 安装包与来源

从干净固定提交构建 Linux amd64、arm64，采用 `-mod=readonly -trimpath -buildvcs=true`，`CGO_ENABLED=0`。四份构建信息中的完整 VCS revision、操作系统、架构、入口模块及 `vcs.modified=false` 均一致；amd64 Agent 与 xbctl 的实际版本输出与完整提交一致。

共 12 份附件：两个架构的 Agent、兼容名称 xboard-node、xbctl，安装脚本、四份构建信息及 SHA256SUMS。12 份均已匿名下载，字节数及 SHA-256 与本地文件和 GitHub 附件摘要一致；11 项 SHA256SUMS 全部通过，兼容名称与主程序字节相同。

| 主程序 | SHA-256 |
| --- | --- |
| yz-agent-linux-amd64 | `2ba1aa2320f0e6391c6a1c3c63f721ceb930585386a853f4d74f8206d83e96f6` |
| yz-agent-linux-arm64 | `9ad7540929d278617f277590391e095327975777308a21a68dec7735e3e74e13` |

详细检查和附件来源见[结构化记录](release-2.36.0-verification.json)。临时工具、隔离 rootfs、测试输出、构建和下载验证文件在交付前清理，正式附件保留在 Release；规则和真实凭据未进入源码或附件。

## 更新与回退

在面板“服务器管理”选择目标服务器并执行“升级”，以机器回报 v2.36.0 确认完成，然后在“路由管理 → 拦截记录”查看后续命中。发布本身不会自动升级运行中的节点，历史拦截记录无法补采。

回退版本为 v2.35.0。旧版继续原代理功能，但不会上报本次新增拦截事件；面板已保存的记录按原期限保留。不要删除配置、累计流量文件或已有缓存。
