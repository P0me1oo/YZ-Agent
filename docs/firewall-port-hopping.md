# 自动防火墙与 Hysteria2 端口跳跃

Node `v1.13-yz.24` 配合面板 `1.13.0` 增加自动端口管理。本功能位于 Node，Xray 与 sing-box 的固定依赖不变。版本发布状态和验证结果以 [兼容矩阵](../YZ_COMPATIBILITY.md) 为准。

## 启动、停用和重载

Linux 主机默认开启自动管理。Node 检查正在运行的 UFW、firewalld，内核成功监听后放行对应 TCP/UDP 端口；面板停用或删除节点、清空普通节点用户、正常退出、启动或重载失败后撤销本进程创建的规则。机器模式通过节点发现同步停用状态，界面的订阅显示开关 `show` 不控制节点运行。

多个节点共用同一个管理器。重复同步不会重复添加规则，同一端口被多个节点使用时，要等最后一个节点停止才撤销放行。范围变化后清理旧范围，只改变跳跃范围不会重建内核监听。管理器每 30 秒恢复缺失规则并重试清理，firewalld 重载后的恢复也使用这个周期。

规则归属保存在配置文件旁的 `firewall/` 目录，权限为目录 `0700`、状态文件 `0600`，其中只有端口和规则标识。写入意图后才执行系统命令。进程异常退出或被 `SIGKILL` 终止时，无法立即执行清理；下一次以同一配置路径启动时先回收遗留规则。修改配置路径或删除状态目录前，应先正常停止 Node。

已有手工放行规则保留，停用节点后这些端口仍可能由手工规则放行。相同端口已存在 UFW 拒绝、限制或另一个 Node 进程的规则时，Node 报告冲突，不覆盖它们。共享引用只覆盖同一 Node 进程；同机多个面板或多个节点应使用 `instances` 或机器模式统一管理。

## 配置

默认无需增加配置。需要指定后端时，在配置文件顶层填写：

```yaml
firewall:
  enabled: true
  backend: auto
  redirect_backend: auto
```

| 字段 | 默认值 | 行为 |
| --- | --- | --- |
| `enabled` | `true` | 同时控制自动放行和跳跃转发；设为 `false` 后清理之前的托管规则，由管理员管理端口 |
| `backend` | `auto` | `auto` 检查 UFW、firewalld；可指定 `ufw`、`firewalld`；`none` 仅关闭放行管理，仍允许跳跃转发 |
| `redirect_backend` | `auto` | 优先使用已安装的 `nft`，没有时使用 `iptables`；也可指定 `nftables` 或 `iptables` |
| `zone` | 空 | firewalld 自动使用活动区域及默认区域；填写区域名时仅管理该区域 |
| `state_dir` | 配置文件目录下的 `firewall/` | 归属记录目录；相对路径以配置文件目录为基准 |

同一进程中所有实例的最终 `firewall` 配置必须一致。`yz-agent` 修改绑定时保留这些字段。Node 不会自动安装或启用防火墙服务；没有运行中的 UFW/firewalld 时不创建过滤规则，跳跃转发仍需要相应的系统工具。

该功能面向具备系统防火墙权限的 Linux 主机安装。UFW 需要 `ufw`，firewalld 需要 `firewall-cmd` 及可用的系统服务；跳跃需要 `nft`，或所用地址族对应的 `iptables`/`ip6tables` 与 `*-restore`。非 Linux 平台不自动管理防火墙，启用自动跳跃会返回不支持。

Docker 的 `--network=host` 只共享网络，不能单独提供宿主机 UFW 配置、firewalld 服务接口或命令权限。需要托管宿主机防火墙时使用主机安装方式；普通容器部署可设置 `firewall.enabled: false`，由宿主机配置放行和转发。云平台安全组仍由云平台管理。

## 在面板设置端口跳跃

按照 [Hysteria 官方端口跳跃说明](https://v2.hysteria.network/zh/docs/advanced/Port-Hopping/)，服务端仍只监听一个 UDP 端口，其余端口由系统转发到该监听端口。

例如，Hysteria2 节点填写：

| 面板字段 | 示例 | 用途 |
| --- | --- | --- |
| 服务端口 `server_port` | `8443` | 内核实际监听的单个 UDP 端口 |
| 连接端口 `port` | `20000-20100,20300` | 客户端可连接的跳跃端口 |
| 跳跃间隔 `protocol_settings.hop_interval` | `5` | 客户端固定间隔，单位秒，设置时必须至少 5 秒；留空使用客户端默认值 |

连接端口支持单端口列表、连续范围及其组合。面板和 Node 校验 `1..65535`、空项和倒序范围，并合并重复、相邻或重叠的范围。面板通过 `port_hopping` 下发跳跃端口，保留单独的 `server_port`；Mihomo 和 URI 订阅保留端口集合，sing-box 订阅转换为合法的多个 `server_ports` 范围。

本轮沿用固定间隔字段，没有新增官方客户端的随机间隔配置。纯单端口连接配置保持原语义，不自动推断外部端口映射。

独立模式在原有节点配置中加入：

```yaml
standalone:
  enabled: true
  node:
    protocol: hysteria
    version: 2
    listen_ip: "::"
    server_port: 8443
    port_hopping: "20000-20100,20300"
```

以上仅展示端口字段，独立模式仍需配置自己的测试或运行身份和证书。

普通 HY2 节点和 HY2 中转入口可使用跳跃，内部落地节点不创建跳跃转发。`listen_ip` 为 `::` 或为空时按双栈管理，`0.0.0.0` 只管理 IPv4，指定地址时只管理该地址。跳跃范围不能把同一 UDP 端口转发到不同节点，也不能覆盖本进程另一个 UDP 节点的真实监听端口。

## 规则实现和范围

UFW 使用带 `yz-agent:` 注释的独立放行规则。firewalld 使用独立优先级的运行时 rich rule 及归属记录，不执行全局重载或修改永久配置。nftables 使用单独的 `inet yz_agent_<标识>` 表，iptables 使用单独的 `YZ_AGENT_<标识>` 链；更新和退出只操作本实例规则。

从旧版升级时，先按归属记录清理 `yzboard-node:`、`yz_node_` 和 `YZH_` 旧规则，成功后再切换新名称；清理失败保留记录供重启重试。默认配置目录搬迁后继续使用原实例标识，避免遗漏旧规则；已有手工规则和其他实例的规则保持原归属。

跳跃只匹配发往本机的 UDP 流量，真实监听端口从转发集合中排除。通配监听使用 REDIRECT，指定地址使用 DNAT。进入本机的流量适用这些规则；本机客户端应直接连接真实监听端口。

自动放行覆盖面板或独立模式定义的主监听，以及中转落地实际监听端口。自定义核心配置额外增加的入站不在自动计算范围内。sing-box SOCKS 的 UDP 会话端口动态分配，自动管理只覆盖它的固定 TCP 监听。

## 验证和升级

原生命令验证使用专用 rootfs、私有挂载/进程/网络命名空间和独立客户端，覆盖 UFW/firewalld 与 nftables/iptables 组合。完整 Node 验证使用临时面板接口、临时身份、自签名证书和官方 Hysteria 客户端，检查真实换端口传输、面板停用、重载错误、状态及流量回传。

复测需要在专用 rootfs 安装相应防火墙工具、iproute2、D-Bus 和 Python，在根目录创建 `.yz-firewall-test-rootfs` 标记。将防火墙 Go 测试二进制放入 `/work/firewall-linux.test`；完整测试还需要 `/work/yz-agent-linux-amd64`、`/work/hysteria-linux-amd64` 和 `scripts/test-firewall-node.py`。测试入口拒绝使用 `/` 或没有标记的目录。

```bash
# 参数依次为专用 rootfs、运行的防火墙、转发工具、测试类型、内核。
sh scripts/test-firewall-linux.sh /path/to/test-rootfs ufw nftables backend
sh scripts/test-firewall-linux.sh /path/to/test-rootfs firewalld iptables node singbox
```

测试实例和身份放在私有 `/run` 内，退出后清理；可复用程序文件，但不能复用测试身份。实际结果、工具版本和限制见 [验证记录](firewall-validation.md)。

升级顺序为 Node、面板、客户端订阅。回滚到没有自动管理的 Node 前，先为旧版准备必要的手工放行/转发，或将 HY2 连接端口改回真实单端口并刷新订阅；正常停止新版完成规则回收后再替换程序。
