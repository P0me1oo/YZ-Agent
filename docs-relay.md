# 中转节点（单入口多落地）

面板把一个 VLESS 或 Hysteria2（HY2）入口节点和若干落地节点组成一组中转拓扑。客户端只连接入口，
订阅里的每个逻辑节点都使用入口的客户端协议、传输方式、地址、端口和传输
安全参数，区别只在写入 UUID 的路由编号。Node 不需要额外的本地配置，全部参数由
面板通过 `relay` 段下发。

面板 `1.11.0` 与 Node `v1.13-yz.21` 的源码实现支持 Xray、sing-box 入口与落地混用。
面板 `1.12.0` 与 Node `v1.13-yz.22` 进一步支持 HY2 ECH 入口。
发布引用、固定核心依赖与验证状态见 [兼容矩阵](YZ_COMPATIBILITY.md)。

## 路由编号

编号是一个 1-65535 的整数，写在客户端 UUID 的第 7、8 字节（0 基下标 6、7），
也就是标准写法的第三段。VLESS 使用身份 UUID，HY2 使用认证 UUID。Xray 校验用户前会把这两个字节清零，因此写入
编号不会影响用户身份匹配；认证通过后内核再按原始字节还原编号，交给路由规则的
`vlessRoute` 匹配项使用。

编号 0 不可用：Xray 的端口列表解析会丢弃数字 0。

sing-box 入口为每个真实用户生成入口自身及各落地的认证身份，再用原生 `auth_user`
规则选择出站，不要求内核理解 Xray 的 `vlessRoute` 字段。线路身份始终映射回原用户的
ID 和 UUID，因此多条线路共享同一份限速、设备限制和套餐计数。两种内核使用相同的订阅格式，
客户端只需支持入口协议。

sing-box 只接受已下发的线路身份：未知编号、已删除用户或已轮换凭据的新请求都会被拒绝，
包括从旧 HY2 QUIC 会话发起的新请求。已有数据流遵循内核原有连接生命周期。

安装器默认写入 xray；已经装好的节点可以用下面的命令切换，改完重启服务生效：

```bash
yz-agent config kernel xray            # 全部实例
yz-agent config kernel xray --instance <实例 ID>
yz-agent config kernel singbox         # 使用 sing-box
yz-agent service restart
```

xray 能承载的入站协议少于 sing-box（不支持 tuic、naive、anytls、mieru、socks、http），
因此切到 xray 时命令会拒绝这些协议的实例，确认要切再加 `--force`。

## HY2 入口

Node `v1.13-yz.20` 配合面板 `1.10.0` 新增 Xray HY2 前置入口；`v1.13-yz.21` 与 `1.11.0`
进一步支持 sing-box。顶层配置使用
`protocol: "hysteria"`、`version: 2`，证书沿用现有 `cert_config`，`relay` 段与 VLESS 入口完全相同。
客户端连接入口的 UDP 端口，入口完成 QUIC/TLS 握手后读取认证 UUID，按其中的编号选择内部出站。

每个已认证 QUIC 会话保留自己的用户和编号；同一用户可以同时通过不同认证值访问不同落地。
客户端只把订阅中的认证值作为普通 HY2 密码使用，无需实现路由编号逻辑。
入口与落地可以分别选择 Xray 或 sing-box，内部链路支持 Shadowsocks 和 VLESS。

可选混淆支持 Salamander，Node 将 `obfs`、`obfs-password` 映射为 Xray 的 UDP 混淆参数，
同时保留带宽参数。混淆密码至少 4 字节。HY1 和其它混淆类型在中转校验时拒绝。
普通 HY2 节点不增加 `relay` 段，面板也不改写其认证值。

### HY2 ECH

Node `v1.13-yz.22` 配合面板 `1.12.0` 支持开启 ECH 的 HY2 前置入口。
入口仍需要有效的 TLS 证书；ECH 不改变中转路由、落地协议、用户热更新和流量报告格式。

面板的 `protocol_settings.tls.ech` 已映射为 Node 顶层 `tls_settings.ech`。
启用时必须提供 `key` 或 `key_path`；Xray 将 PEM／Base64 密钥写入 `tlsSettings.echServerKeys`，
sing-box 使用原生 `tls.ech.key`／`key_path`。内联密钥优先，Xray 读取或解析失败时返回配置错误，
不会因缺少密钥而省略 ECH 后继续生成普通 HY2 配置。sing-box 的文件和格式校验由固定内核完成。

客户端选择 sing-box JSON 或支持 ECH 的 Mihomo YAML 订阅，使用入口公共配置和各逻辑节点的认证值。
服务端私钥只下发给入口。Mihomo 实测最低版本为 `1.19.9`；应用外壳版本不能替代内核版本。
本次没有为通用 HY2 分享 URI 声明 ECH 参数支持。

本地实测覆盖 Xray／sing-box 入口及落地混用、TCP/UDP、Salamander、错误公共配置拒绝、
ECH 密钥轮换、用户与节点生命周期及精确计数，详情见 [验证记录](docs/hy2-ech-validation.md)。

## 入口节点配置

面板下发：

```json
{
  "protocol": "vless",
  "server_port": 24443,
  "relay": {
    "mode": "entry",
    "route_id": 11,
    "children": [
      {
        "node_id": 7,
        "tag": "relay-7",
        "route_id": 12,
        "protocol": "shadowsocks",
        "address": "203.0.113.7",
        "port": 28388,
        "cipher": "2022-blake3-aes-128-gcm",
        "password": "<内部凭据>"
      }
    ]
  }
}
```

Node 据此生成：

- 一个真实的 VLESS 或 HY2 入站，沿用面板节点配置的传输安全和监听端口；
- 每个逻辑节点一个独立的 Shadowsocks 或 VLESS 出站，标签为 `relay-<逻辑节点 ID>`；
- 每个编号一条路由规则，入口自身的编号指向 `direct`。

`children[].protocol` 也可以是 `vless`。这时同一项会带 `vless` 对象，其中包含面板派生的
内部 UUID、传输、传输安全、Flow、客户端 `encryption` 和必要的公有 TLS/Reality 参数。
Reality 私钥、服务端 `decryption` 和证书不会出现在入口 child 中。

逻辑节点数量不影响入站数量，入口上永远只有一个客户端入站。

### 路由规则优先级

生成的规则按以下顺序排列：

1. 结构化 `custom_route_rules`
2. 原始 `custom_routes`
3. 内置私有地址与回环地址拦截
4. 中转路由规则
5. 面板路由组

中转规则排在面板路由组之前，保证选中某个逻辑节点时出口稳定；管理员显式配置的
自定义规则和内网拦截仍然优先。
Xray 未匹配路由编号时继续使用原有默认出站。sing-box 未注册的线路身份会认证失败，
不会退回默认出站；管理员自定义路由仍可覆盖已认证请求的出口。

## 落地节点

面板下发：

```json
{
  "protocol": "shadowsocks",
  "server_port": 28388,
  "relay": {
    "mode": "landing",
    "protocol": "shadowsocks",
    "listen_port": 28388,
    "cipher": "2022-blake3-aes-128-gcm",
    "password": "<内部凭据>",
    "entry_node_id": 3
  }
}
```

Node 生成一个标签为 `relay-in` 的 Shadowsocks 入站，只包含这一份内部凭据，
不包含任何面板用户，同时开启 `tcp,udp`。落地服务器上的 Xray 或 sing-box 直接向目标建立
连接，不需要开启系统转发或出口地址转换。

VLESS 落地的 `relay` 段改为 `protocol: "vless"`，并在 `relay.vless.id` 下发同一内部 UUID。
传输、TLS/Reality、Flow、证书和 `decryption` 继续使用落地节点顶层配置，Hysteria 额外使用
`relay.vless.transport_auth`。Node 生成一个单用户 VLESS `relay-in` 入站，该用户没有面板
用户 ID 或统计邮箱，因此不会进入用户套餐流量。

因为落地节点没有面板用户，Node 对这类节点放宽了“用户列表为空就停止内核”的
规则：空用户集是落地节点的正常状态，内核会保持监听。

## 支持的内部协议

### Shadowsocks 加密算法

`2022-blake3-aes-128-gcm`、`2022-blake3-aes-256-gcm`、`2022-blake3-chacha20-poly1305`、
`aes-128-gcm`、`aes-192-gcm`、`aes-256-gcm`、`chacha20-ietf-poly1305`、
`xchacha20-ietf-poly1305`。

内部凭据由面板派生后分别下发给两端，不写入 Node 的本地配置文件，也不出现在
客户端订阅中。

### VLESS 传输矩阵

以下矩阵适用于两端均为 Xray 的内部链路，VLESS 客户端入口也按其自身内核校验：

| 传输 | 无传输安全 | TLS | Reality |
| --- | --- | --- | --- |
| RAW / TCP | 支持 | 支持 | 支持 |
| WebSocket | 支持 | 支持 | 不支持 |
| gRPC | 支持 | 支持 | 支持 |
| XHTTP | 支持 | 支持 | 支持 |
| HTTPUpgrade | 支持 | 支持 | 不支持 |
| mKCP | 支持 | 支持 | 不支持 |
| Hysteria | 不支持 | 支持 | 不支持 |

任一端使用 sing-box 时，内部链路取两端能力的交集：

| 传输 | 无传输安全 | TLS | Reality |
| --- | --- | --- | --- |
| RAW / TCP | 仅内网 | 支持 | 支持 |
| WebSocket | 仅内网 | 支持 | 不支持 |
| gRPC | 仅内网 | 支持 | 支持 |
| HTTPUpgrade | 仅内网 | 支持 | 不支持 |

sing-box 不支持本功能中的 XHTTP、mKCP、Hysteria 传输、VLESS Encryption 或 TCP 头部伪装。
Vision 只允许 RAW/TCP 配合 TLS 或 Reality。公网 VLESS 内部链路必须使用 TLS/Reality；
Shadowsocks 继续使用上文的加密算法。

H2/HTTP 已由固定的 Xray 核心移除。mKCP 不接受旧 `header`/`seed`，Flow 只允许空值或
`xtls-rprx-vision`。Node 会在构建配置前二次校验这些组合，不能依靠忽略未知字段启动。

两端均为 Xray 时，VLESS Encryption 的两项由面板拆分下发：入口 child 只带 `encryption`，落地顶层只带
`decryption`。YZ-Agent 不负责生成这对配置；面板 `1.4.0` 可以在浏览器中复用 Reality
的 X25519 生成器，管理员点击 `decryption` 右侧的钥匙按钮即可一次填入两项。需要
ML-KEM-768 时仍可运行兼容 Xray 的 `xray vlessenc` 后手工填写，Node 不需要修改本地
`config.yml`。

## 流量统计

- 用户流量仍然只在入口按真实 VLESS/HY2 用户身份统计一次，通过 `traffic` 上报。
- 入口按各 `relay-<ID>` 出站归属流量，通过 `relay_traffic` 上报，键为
  逻辑节点 ID。面板把它记入对应落地节点的节点流量，不参与用户套餐扣费。
- 落地节点的内部入站没有面板用户，因此不会重复上报用户流量。
- `relay_user_traffic` 按真实用户和逻辑节点记录原始流量明细，不额外扣费；HY2 与 VLESS 使用相同结构。

Xray 读取内部出站计数，可能包含协议封装；sing-box 在用户连接的 TCP/UDP 读写事件上，
按实际选中的出站累计有效载荷。因此两种入口的落地总量统计口径可能不同，不影响用户扣费。
自定义规则把某条线路改为直连时，sing-box 不把该流量计入原计划的落地。
Node `v1.13-yz.21` 使用 Xray `v26.7.11-yz.6`，包含 VLESS 首批缓冲上传计数、UDP 缓存及 HY2 会话关闭同步修复；
sing-box 配套 `v1.14.0-yz.2`，SS2022 原模块的关闭补丁随 Node 固定源码发布。完整验证见 [兼容矩阵](YZ_COMPATIBILITY.md)。

## 幂等与生命周期

配置哈希覆盖整个 `relay` 段，重复同步同一份配置不会重建实例，也不会产生重复的
出站或路由规则。逻辑节点被禁用或删除后，面板不再下发对应的 child，入口重新生成
配置时该出站和路由规则一并消失。sing-box 的用户增删使用热更新，拓扑变化则按现有规则重建
内核实例，相关连接可能重新建立；用户和线路累计计数不会清空。

sing-box 的内部认证项数量为「用户数 ×（落地数 + 1）」，入站和监听端口仍只有一个。
用户数量或落地数量增加时，配置生成和热更新的内存开销随之增加，实测方法见
[双内核中转验证](docs/singbox-relay-validation.md)。

启用前先升级所有相关 Node，再升级面板并创建或切换中转拓扑；旧 Node 不支持 sing-box 中转。
