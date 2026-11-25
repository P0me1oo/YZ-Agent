# YZ-Agent v2.51.0 发布

日期：2026-11-26。维护者：P0me1oo。

[v2.51.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.51.0) 已发布并设为 Latest，固定源码 `c93f3b3102086b654aeb320f831e3c3993696ee0`。基于已发布 v2.50.0，仅合入日志原文保留变更及测试文档，保留落地 DDNS；不包含主开发目录的 v2.49.0 Next 访问策略工作。两核心依赖、文件加密和隐私出口实现未变。

[固定来源 CI](https://github.com/P0me1oo/YZ-Agent/actions/runs/45672883483) 通过完整竞态回归：1,198 个顶层用例通过，9 项按环境条件跳过；四组 UFW/firewalld 与 nftables/iptables 原生验证、Mihomo 客户端检查、安装检查、双架构构建全部通过。Docker 发布任务未执行。见[检查记录](release-v2.51.0-ci.json)。

12 份正式附件匿名下载，大小、SHA-256、GitHub 摘要和清单一致，兼容文件名与 Agent 二进制逐字节一致；构建信息匹配架构、固定来源与 vcs.modified=false。见[附件核验](release-v2.51.0-verification.json)。

Solid-US LA 面板已更新 2.30.0，45 台可管理节点通过面板升级并回报 v2.51.0，升级结果和近期心跳已确认。3 台离线及 1 台不支持远程管理保持原版；Next 独立测试实例单独记录部署结果。隐私保护状态未改变。回退程序来源 v2.50.0，旧版会恢复日志正文隐藏。
