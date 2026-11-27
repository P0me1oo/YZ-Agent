# Next 服务端路由与访问策略

本地开发版本 v2.49.0，配套 Next / Dash 0.25.0，未发布或部署。固定两核心依赖不变。

Next 下发 `access_policy_version: 1`，可信来源使用 `trusted_proxy_ips`，仅这些来源可以声明 PROXY 客户端 IP。旧面板没有版本字段的节点保留旧兼容行为；不应把旧版接收开关等同于可信来源校验。

全局设备排除现在作用于 Xray、sing-box 和普通 WG，并退役缓存设备许可。Next 名单变更参与配置哈希并重建连接，取消豁免后重新准入；账户权限、连接限制和业务计费继续执行。

Next 路由引用专用出口标签，显式完整域名与后缀域名在两核心中保持一致。禁令先于中转，非拦截不能覆盖已授权中转线路。Xray 明确保持 direct 为默认出口；sing-box 仅需域名匹配的分流在本地 DNS 解析前执行，IP 条件仍按顺序匹配。

本地自定义路由覆盖和 Next 路由同时存在时停止并报告，避免本地规则绕过面板禁令。可信代理只从系统来源集合豁免，核心仍按声明客户端执行原来源策略。原生 UDP 不接收 TCP PROXY 头。

完整边界、撤销及恢复见[Next 说明](../../YZboard-Next/YZboard-Next/docs/server-policies.md)，测试见[验收](../../YZboard-Next/YZboard-Next/docs/server-policies-acceptance.md)。
