# Custom Routes

## Quick Example

```json
{
  "custom_route_rules": [
    {
      "name": "direct-example",
      "match": {"domain_suffixes": ["example.com"]},
      "action": {"type": "direct"}
    },
    {
      "name": "block-ads",
      "match": {"domains": ["ads.example.com"]},
      "action": {"type": "block"}
    },
    {
      "name": "route-warp",
      "match": {"ip_cidrs": ["1.1.1.0/24"], "ports": ["80", "443"]},
      "action": {"type": "route", "target": "warp-out"}
    }
  ]
}
```

## Match Conditions

| Condition | Description | Example |
|-----------|-------------|---------|
| `domains` | Exact domain match | `["api.example.com"]` |
| `domain_suffixes` | Suffix match | `["example.com"]` |
| `ip_cidrs` | IP CIDR ranges | `["10.0.0.0/8"]` |
| `ports` | Port (single or range) | `["443", "8000-9000"]` |
| `networks` | Protocol | `["tcp"]` or `["udp"]` |
| `source_cidrs` | Source IP CIDR | `["192.168.1.0/24"]` |
| `source_ports` | Source port | `["1024-65535"]` |

## Action Types

| Action | Description |
|--------|-------------|
| `{"type": "direct"}` | Direct connection, bypass proxy |
| `{"type": "block"}` | Block connection |
| `{"type": "route", "target": "tag"}` | Route to specified outbound (by tag) |

## Application Order

1. Structured `custom_route_rules` (highest priority)
2. Raw `custom_routes`
3. Built-in blocklist rules
4. Relay rules for logical nodes (relay entry only)
5. Panel routes
6. Relay entry's own direct rule (relay entry only)

## 面板路由

面板“路由管理”下发的 `routes` 与上面的自定义规则相互独立，排在内置内网拦截和中转选路之后。每条路由由目标地址列表 `match`（域名或 IP 网段）、动作和以下可选条件组成（面板 `1.26.0` 起）：

| 字段 | 含义 | 示例 |
|------|------|------|
| `protocol` | 内核嗅探识别出的协议，目前只支持 `bittorrent` | `["bittorrent"]` |
| `port` | 目标端口或范围，逗号分隔 | `"25,6881-6889"` |
| `network` | 只匹配 `tcp` 或 `udp`，缺失表示不限 | `"udp"` |

- 一条路由里的各种条件必须同时满足才命中；同一条件的多个值任一命中即可；域名和 IP 之间仍是任一命中。只有附加条件、没有目标地址的路由对全部目标生效。
- 无法识别的协议、端口或网络值会让整条路由跳过并记录告警，不会丢掉条件后扩大范围。
- BT 依靠内核嗅探：Xray 识别 TCP 握手和 uTP 建连包，sing-box 另外识别 UDP Tracker。加密的 BT 连接和 DHT 无法识别。
- 只有绑定了 `protocol` 条件的节点才开启嗅探。Xray 在入站开启嗅探但不改写目标地址；sing-box 在第一条按协议匹配的规则前插入一次 `sniff`（只用 BT 识别器）。开启后，由服务器先发数据的连接建立时最多多等约 0.2 秒（Xray）或 0.3 秒（sing-box）。
- 中转入口节点绑定的路由作用于入口自身用户；经入口转往落地的流量只受落地节点绑定的路由约束。

```json
{
  "routes": [
    {"id": 1, "match": [], "action": "block", "protocol": ["bittorrent"]},
    {"id": 2, "match": [], "action": "block", "port": "25,465,587", "network": "tcp"}
  ]
}
```

独立运行模式的 `standalone.node.routes` 使用相同的 `protocol`、`port`、`network` 字段。

## Kernel Compatibility

| Feature | Xray | Sing-box |
|---------|------|----------|
| All match conditions | ✅ | ✅ |
| direct / block / route | ✅ | ✅ |

## Best Practices

- **Prefer** `custom_route_rules`: cross-kernel compatible, panel-managed
- **Use** `custom_routes` only: when native features are needed (e.g., load balancing)
