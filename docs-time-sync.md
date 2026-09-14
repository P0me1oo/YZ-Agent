# SS2022 时间校准

Shadowsocks 2022 会使用 Unix 时间戳做重放防护。Unix 时间本身不受服务器时区影响，但前置与落地服务器的系统时间相差过大时，SS2022 握手仍可能被拒绝。

YZ-Agent 默认启用进程内时间校准。它不会修改 Linux 系统时间，也不会安装、启动、停止或替换 `systemd-timesyncd`、chrony、ntpd 等系统服务。

## 生效范围

同一 Node 进程只维护一份校准结果，并同时提供给 Xray 和 sing-box。以下链路会使用校准时间：

- 普通 Shadowsocks 节点使用 `2022-blake3-*` 加密方式；
- VLESS 前置的中转 child 使用 Shadowsocks 2022 出站；
- 落地节点使用 Shadowsocks 2022 中转入站。

例如：

```text
客户端 -- VLESS --> 前置 Node -- Shadowsocks 2022 --> 落地 Node
```

客户端到前置的 VLESS 不依赖 SS2022 时间戳；需要校准的是前置的 SS2022 出站和落地的 SS2022 入站。使用传统 Shadowsocks 或 VLESS 中转时，不会因为本功能输出 SS2022 时钟告警。

## 工作方式

Node 默认并行查询以下 NTP 源：

- `time.cloudflare.com`
- `time.google.com`
- `pool.ntp.org`

每个响应都会经过 NTP 层级、同步状态、新鲜度和误差范围校验。Node 对有效样本按偏移排序并使用中位数，降低单个时间源异常造成的影响。

默认每 10 分钟重新查询，单个时间源超时为 3 秒。查询失败时，Node 最多在三个查询周期内沿用最近一次有效偏移；超过该时间后自动退回系统时间，不会猜测或累加未知偏移。

默认状态阈值：

| 绝对偏移 | 状态 |
| --- | --- |
| 小于 5 秒 | `normal` |
| 5 秒至小于 15 秒 | `warning` |
| 15 秒至小于 25 秒 | `error` |
| 25 秒及以上 | `critical` |

只有当前进程实际承载 SS2022 入站或受管中转出站时，异常状态才会触发相关日志和整体健康降级，避免传统协议节点产生无关告警。

## 配置

```yaml
time_sync:
  enabled: true
  servers:
    - time.cloudflare.com
    - time.google.com
    - pool.ntp.org
  interval: 600
  timeout: 3
  warn_offset: 5
  error_offset: 15
  critical_offset: 25
```

`interval`、`timeout` 和三个偏移阈值的单位都是秒。多实例共享同一进程时，各实例解析后的 `time_sync` 必须完全一致，否则 Node 会拒绝启动。

显式设置 `enabled: false` 会关闭运行时自动校准。若此时节点实际使用 SS2022，健康接口会显示时钟功能已关闭且当前链路需要它。

## 健康检查

`/healthz` 会增加 `clock` 字段：

```json
{
  "status": "degraded",
  "total": 1,
  "running": 1,
  "starting": 0,
  "failed": 0,
  "clock": {
    "enabled": true,
    "required": true,
    "status": "warning",
    "offset_ms": 8300,
    "source": "time.cloudflare.com",
    "last_success": "2026-09-02T12:00:00Z"
  }
}
```

时钟异常会把 JSON 中的整体状态标为 `degraded`，但仍返回 HTTP 200，避免容器或服务管理器因临时 NTP 故障反复重启。节点组件启动中或运行失败时仍返回 HTTP 503。

## 主动诊断

```bash
yz-agent doctor time
yz-agent doctor time --config /etc/yz-agent/config.yml --output json
```

该命令直接查询配置中的 NTP 源并输出偏移、来源和状态，不需要面板 Token，也不修改系统时间。即使运行时自动校准被关闭，诊断命令仍会执行一次只读探测。探测失败或状态不是 `normal` 时返回非零退出码。

若全部 NTP 查询失败，应检查 DNS、出站 UDP 123、防火墙和上游网络。系统时间服务仍建议正常配置，但不属于 Node 自动管理范围。
