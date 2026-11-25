# YZ-Agent v2.48.0 发布

日期：2026-11-25。维护者：P0me1oo。

[v2.48.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.48.0) 已发布并设为 Latest，固定源码 `1017613a04454362a08b3c3a0f19e07bb3d5a6e5`。汇总 v2.44.1—v2.47.0 未正式发布改动，配套 YZboard 2.28.0 / Dash 0.110.0，兼容 Next 0.24.0。来源限制、失败保留及双栈边界见[说明](relay-firewall.md)；新增转发、本机目标授权和快照分块见对应文档。

[固定来源 CI](https://github.com/P0me1oo/YZ-Agent/actions/runs/45601926300) 完整竞态 1,149 个顶层用例通过，9 项隔离或端口环境用例按条件跳过；固定依赖 43 项、Mihomo 2 项和 10 组安装检查通过，四组 UFW/firewalld 与 nftables/iptables 原生检查通过，Linux amd64/arm64 构建及来源核验通过。详细步骤见[CI 记录](release-v2.48.0-ci.json)及[测试统计](release-v2.48.0-tests.json)。复用相同功能源码的中转专项原生回读/写盘失败恢复、异常重启和 Next 双核心快照验收，不将默认跳过的隔离用例混计为普通回归通过。

13 份附件已匿名下载，大小、本机 SHA-256、GitHub 摘要和 SHA256SUMS 全部匹配；Agent 与兼容文件名逐字节一致，构建信息匹配固定提交、两个架构、CGO_ENABLED=0 与 vcs.modified=false，见[附件核验](release-v2.48.0-verification.json)。正式核心依赖保持 YZ-sing-box v1.16.0、YZ-Xray-core 固定提交 36f4516090e3。

本次未更新运行节点、未发布 Docker 镜像，Next 面板未在本次发布。通过面板“服务器管理”升级；先更新面板和全部前置，再升级落地，以操作结果和回报版本确认。旧版参考 v2.44.0；回退前核对新增转发和来源策略，正常停止新版完成本实例清理。
