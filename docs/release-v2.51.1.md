# YZ-Agent v2.51.1 发布

日期：2026-11-26。维护者：P0me1oo。

v2.51.1 已正式发布并设为 Latest，固定源码 `091aa0d886ccbf6fa286fec189f4f869fd71b9b0`，配套生产面板 2.30.2。IPv6 能力、防火墙归属及通信诊断见[修补说明](runtime-errors-20261126.md)。核心依赖不变，不包含主工作区 Next 开发改动。

CI `45769448333` 最终成功。第一次运行仅既有系统 WireGuard 端口占用恢复用例失败；该代码与 v2.51.0 相同，源码未改动，重跑失败任务后完整通过。四组 UFW/firewalld 与 nftables/iptables 原生检查复用首次成功结果。最终 Run tests 日志包含 1207 个顶层通过用例、8 个环境条件跳过，Mihomo 互通、安装脚本语法及双架构构建检查成功。Docker 发布任务跳过。

12 份正式附件全部匿名下载，文件长度、GitHub 摘要、SHA256SUMS 和本地 SHA-256 一致；两个架构的 Agent 与兼容文件名逐字节相同。四份构建信息核对固定提交、架构、CGO_ENABLED=0、vcs.modified=false，amd64 版本运行输出正确。详见[附件核验](release-v2.51.1-verification.json)及[工作流记录](release-v2.51.1-ci.json)。

运行节点通过 YZboard 管理接口升级，46 台受理机器全部 succeeded 并回报 v2.51.1；Aliyun-香港¹离线未受理。Aether-美国首次启动领取文件保护密钥短暂失败并回滚，通过面板单台重试后成功。Zouter 的纯 IPv4 来源过滤和 Novixlink 的双栈规则均已核验。最终任务回报记录在面板部署文档。
