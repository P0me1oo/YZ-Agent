# YZ-Agent v2.37.1 发布记录

维护者：P0me1oo。日期：2026-11-13。

[v2.37.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.37.1) 已发布并设为 Latest，固定源码 `c9007ed74c6db568b652d3c00cb98be93c56da4e`，配套面板 2.16.6。用户明确选择在 WebUI 自行更新 Agent，本次未升级或重启任何运行节点。

Go 1.27.3，正式构建标签和竞态检测：41 个包共 1,010 个顶层用例，1,004 个通过、6 个按环境条件跳过。首次一项 Hysteria2 测试因本机 UDP 端口已占用失败，在独立网络空间重跑通过，其余同源码成功结果复用。五个原生防火墙用例/辅助项随后在独立 Debian 13 rootfs、网络与进程空间验证；UFW / firewalld × nftables / iptables 的三种场景共 12 项通过。另一项为既有随机回环端口范围条件检查。

八个依赖兼容包、八组安装器检查、固定 Mihomo v1.19.31 的两项 WireGuard 联调通过。临时 rootfs 的设备节点挂载及 logind 依赖补齐后原生测试通过，没有改动宿主防火墙。

Linux amd64 / arm64 的 Agent 与 xbctl 均核对固定版本、提交、架构、`CGO_ENABLED=0` 和 `vcs.modified=false`；xboard-node 兼容文件与对应 Agent 字节一致。安装器、六个程序文件、四份构建元数据和校验清单共 12 份附件全部下载匹配。amd64 Agent 与校验清单另经匿名下载核验。

分支工作流 `43393448728` 因账单锁定未启动，未计为通过；未发布 Docker 镜像。通过面板“服务器管理”选择目标机器执行“升级”，以回报 **v2.37.1** 确认完成，回退安装包为 **v2.37.0**。

[设备锁竞争恢复](device-lock-retry.md)说明协议与兼容范围。后补文档不移动标签或改写已发布附件。
