# 中转落地端口来源限制

v2.48.0 已正式发布，配套 YZboard 2.28.0 / Dash 0.110.0，兼容 Next 0.24.0；未升级运行节点，Next 面板不在本次发布范围。固定源码和附件见[发布记录](release-v2.48.0.md)，下文开发记录保留其原验证阶段。

Agent v2.48.0 配套 Next / Dash 0.24.0，在握手声明 `relay_firewall=1`，必要实时状态通过 `state.relay_egress` 发送测量；旧的指标上报格式仍兼容。运行状态增加 `operations.relay_firewall`（applied/pending/disabled），与 `firewall_warning` 一起供管理端判断执行结果。来源变化不重载两核心。

Next 规则和证据有效期见[Next 使用说明](../../YZboard-Next/YZboard-Next/docs/relay-firewall.md)，本地双端、双核心及原生防火墙结果见[验收](../../YZboard-Next/YZboard-Next/docs/relay-firewall-acceptance.md)。当前源码未发布、未部署，正式核心依赖保持不变。

Agent v2.47.0 配套面板 2.28.0 / Dash 0.110.0，当前为本地开发，未发布、未部署。完整范围、核对条件、刷新频率、失败保留及升级顺序见[面板说明](../../YZboard/docs/relay-firewall.md)，验证见[记录](../../YZboard/docs/relay-firewall-validation.md)。

新策略使用 `relay.firewall.version=1`，旧面板没有版本号的字段继续沿用原行为。面板提供带身份指纹的 `relay_egress_targets`；Agent 每 30 秒读取全部目标的系统选路来源，以 `relay_egress` 报告完整性，任一目标地址解析或选路失败都不能发布部分来源。

策略使用精确 IPv4/IPv6 地址，不能填写网段或主机名。当前名单与节点自己的来源限制取交集，防火墙只在实际中转监听端口丢弃其他来源。启用前安装保护，核心启动后沿用既有端口放行；来源与测量目标变化不重建核心。过期策略不授权新写入，已有规则继续保留。

Linux 使用已有 nftables，或完整的 iptables、ip6tables、restore 和 ipset 工具。两个后端均核对归属，失败恢复本实例来源规则；缺工具、无权限和非 Linux 环境报告未生效，不静默成功，不为本功能安装工具。显式关闭全局或节点自动管理开关仍按原设置处理。

规则核对失败经 `firewall_warning` 和节点运行状态上报；核心仍在运行时保持运行状态，管理端另显示警告。正常停用、解绑和退出仍清理本实例托管规则。断联、待核对与异常重启时不认领或删除手工规则；重启恢复等待全部节点发现，并保留待核对端口的原限制。
