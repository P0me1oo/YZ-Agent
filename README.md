# YZ-Agent

正式版 `v2.22.0` 配套面板 `1.101.0`、管理端 `0.72.0`：默认接管节点端口，来源限制以面板为准；节点高级设置可关闭自动管理，撤回本节点托管规则，原手工规则保持原样。来源过滤先核验、监听成功后才接管放行，UFW 注释显示面板节点 ID。发布与双架构核验见[兼容记录](YZ_COMPATIBILITY.md)，使用见[自动防火墙说明](docs/firewall-port-hopping.md)。

`v2.22.1` 发布准备：系统来源过滤优先使用 nftables，Linux root 主机仅在需要且缺少工具时自动补装；来源限制关闭的普通节点不触发安装。已有托管后端和显式选择保留，安装失败不绕过来源保护。正式发布状态见[兼容记录](YZ_COMPATIBILITY.md)，使用见[来源限制](docs/source-policy.md)。

Agent v2.20.0 配套面板 1.99.0、管理端 0.70.0，新增节点来源 IP 黑名单、白名单及黑名单一键屏蔽大陆 IP，兼容旧来源策略。见 [来源限制](docs/source-policy.md)。

Agent `v2.19.3` 修复防火墙命令超时后的子进程清理，并将安装、升级和失败恢复的服务就绪等待上限调整为 180 秒。沿用 `v2.19.2` 的面板接口、内嵌核心及固定依赖。

Agent `v2.19.2` 将 Xray `v26.10.17` 编入正式程序，使用 YZ-Xray-core `v26.10.0` 的固定提交。保留 YZ 的流量、限速、SS2022 时间校准与连接释放、PROXY 接收和 REALITY 客户端兼容；配套面板 `1.93.0`、管理端 `0.65.0`，sing-box 继续使用 `v1.15.0`。来源和验证结果见[兼容矩阵](YZ_COMPATIBILITY.md)。

Agent `v2.18.0` 增加升级镜像自动选择：GitHub 直连超时后，并行探测 `gh-proxy.org`、`ghfast.top` 和 `github.chenc.dev`，按实际 HTTPS 响应耗时选择可用源。覆盖最新版查询、校验清单及程序下载；所有来源均无校验清单时，提示并自动跳过校验继续升级。详情见下方升级说明。

Agent `v2.17.0` 支持服务器保护“仅计费代理流量”，配套面板 `1.93.0`、管理端 `0.65.0`。使用面板去重持久化的原始代理用量，不读取网卡；保留模式切换、每月重置、超限停服及恢复，见[使用说明](docs/server-traffic-protection.md)。

[`v2.16.1`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.16.1) 已发布，将访问记录归属改为实际出口：中转记录选中的落地，前置直出记录前置。配套面板 [`1.92.0`](https://github.com/P0me1oo/YZboard/releases/tag/v1.92.0)；包含下方 `v2.16.0` 的接收开关改动。见[访问记录](docs/access-logs.md)。

原开发版本 `v2.16.0` 的节点级“接收 PROXY 协议”开关已纳入 `v2.16.1`，配套面板 `1.92.0`、管理端 `0.64.0`。本地修复 `v2.21.1` / 面板 `1.100.1` / 管理端 `0.71.1` 统一为明确开关，旧节点不再按前置名单决定接收。开启后自动识别任意来源的 TCP PROXY v1/v2 并兼容普通连接，无需填写中转 IP；请配合防火墙限制使用。两个核心无需修改，尚未发布。见[接收说明](docs/proxy-protocol.md)。

[`v2.15.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.15.0) 已发布，为 sing-box 的通用 TCP 入站增加可信 PROXY protocol v1/v2 接收，配套核心 `YZ-sing-box v1.15.0`。沿用面板“前置服务器 IP”设置指定可信 Realm 出口；普通直连不受影响，原生 UDP、QUIC 不在此次支持范围内。配置方式、验证结果和限制见[真实来源说明](docs/proxy-protocol.md)及[兼容矩阵](YZ_COMPATIBILITY.md)。

[`v2.14.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.14.0) 已发布，配套面板 `1.88.2`、管理端 `0.60.2`。Reality 检测增加 CDN 厂商识别和页面状态，位置由面板本地 ip2region 查询；保留旧任务兼容及原握手判断，两个核心和固定依赖不变。完整 CI、12 个附件和双架构镜像已核验，先更新面板，再更新 Node。详见[检测说明](docs/reality-target-scan.md)和[发布记录](YZ_COMPATIBILITY.md)。

`v2.13.0` 新增代理访问记录，配套面板 `1.86.0`、管理端 `0.58.0`。只记录用户、域名或 IP、端口和连接时间，支持批量确认与缓存恢复；范围和保存限制见[访问记录](docs/access-logs.md)。

[`v2.12.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.12.0) 已发布，配套面板 `1.85.0`、管理端 `0.57.0`。新增节点服务器执行的 Reality 目标检测，支持域名、IP 和指定网段，通过原控制连接返回延迟与结果；预设去掉 Cloudflare，两个核心不变。包含此前未独立发布的 `v2.11.1` 出口网卡识别修复；12 个附件及双架构镜像已核验，先更新面板，再执行 `yz-agent upgrade`。见 [Reality 目标检测](docs/reality-target-scan.md)与[发布记录](YZ_COMPATIBILITY.md)。

[`v2.11.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.11.0) 已发布，配套面板 `1.82.0`，新增服务器网卡流量保护。首次启用从 0 开始，无手动校准；超限关闭全部受管代理并保留管理连接。Linux 完整检查、12 个附件和双架构镜像均已核验，使用及恢复规则见[服务器流量保护](docs/server-traffic-protection.md)，来源与校验值见[兼容矩阵](YZ_COMPATIBILITY.md)。

正式版本 [`v2.9.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.9.0) 已发布，配套面板 `1.57.0`：设备协调复用长连接、稳定来源轻量续期、系统采样共享，并为发送积压设置限额。Linux 完整竞态、客户端联调和四组防火墙检查通过；12 个附件、双架构镜像和 `latest` 已核验。先更新面板，再更新 Node；实现、固定来源和回滚参考见 [运行开销优化](docs/runtime-optimization.md) 与 [兼容矩阵](YZ_COMPATIBILITY.md)。

正式版本 [`v2.8.0`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.8.0) 已发布，配套面板 `1.56.0`：设备名额满时替换最久没有发起新连接的旧来源，实际关闭后再放行新 IP；旧来源冷却 60 秒，有空位可提前恢复。两个核心与普通 WireGuard 共用跨节点协调，先更新面板，再更新全部相关 Node。Linux 完整竞态回归、双架构附件、镜像和 `latest` 已核验，规则与固定来源见 [设备计数](docs/device-counting.md) 和 [兼容矩阵](YZ_COMPATIBILITY.md)。

正式版本 [`v2.7.1`](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.7.1) 新增普通 WireGuard 接入，配套面板 `1.53.0`、管理端 `0.30.0`。两种核心均支持用户权限、流量、限速、到期停用、按公网 IP 计算的设备限制和大陆来源拦截。保留原 WG 中转，包含原 `v2.6.0` 的来源策略改动；固定核心依赖不变。双架构附件、镜像和 latest 已核验，详见 [兼容矩阵](YZ_COMPATIBILITY.md)、[普通 WG](docs/wireguard-direct.md) 与 [来源拦截](docs/source-policy.md)。

`v2.5.0` 让 Linux Xray 直接接入已有 WG 隧道，移除额外的本机转接，保留 BBR 和线路隔离。MTU 支持 1280–1500，配套面板 `1.46.0`、管理端 `0.23.0` 默认使用 1420；已有值不覆盖。固定核心依赖不变，两种核心可混用。测试和发布记录见 [兼容矩阵](YZ_COMPATIBILITY.md)。

`v2.4.3` 在面向用户的 Xray VLESS TCP 入口开启断线探测，并恢复普通 TCP 的默认监听方式，减少换网后失效连接占用设备名额的时间。保留原有设备上限和健康闲置连接，继续沿用 `v2.4.1` 的面板配套要求。适用范围与验证限制见 [设备计数](docs/device-counting.md)，正式发布状态见 [兼容矩阵](YZ_COMPATIBILITY.md)。

保留此前的用户实时网速、自动端口放行等修改；不再使用中转来源 IP 白名单、出口核对与确认提示，升级后仍会清理本实例的旧来源规则。

本版同时纳入原计划 `v2.2.0` 的用户上传、下载速度上报，复用已有秒级采样，不保存历史或改变计费。保留 [自动端口放行](docs/firewall-port-hopping.md) 和 `v2.0.0` 的系统 TCP WG 格式；从旧 WG `1.x` 升级仍要求入口、落地配套更新，详见 [系统 WG 说明](docs/system-wireguard.md)。固定依赖和回滚基线见兼容矩阵。

包含 `v1.26.2` 的相同配置误重载修复，该修复兼容现有 `1.33.0` 及后续同接口面板。无需重新创建节点或修改订阅。

内嵌 Xray fork `v26.9.3`，修复 WireGuard 前置与落地启动时的并发初始化问题；sing-box fork 保持 `v1.14.0-yz.2`。核心随 Node 一起更新，无需另行安装。

保留 `v1.25.1` 的实时通信与版本命令测试修正：新面板下运行状态以每秒采样为目标，WebSocket 失败后默认十秒 HTTP 兜底，流量仍按原批次周期落盘与确认。

`v1.24.0`：两种内核都按用户采集真实活跃连接数；中转入口另按实际出网节点拆分，同一来源 IP 下的多条连接分别计数。节点报告新增 `connection_counts` 与 `relay_connection_counts`；原有在线来源、设备数和流量口径不变。

`v1.23.0`：中转入口按用户实际出网的节点（落地或入口直连）拆分在线来源，随节点报告以 `relay_user_alive` 上报，供面板插件把连接数、在线设备数记到用户实际使用的节点上。设备数限制、在线人数和流量上报不变。说明见 [docs-relay.md](docs-relay.md#流量统计)。

`v1.22.0`：面板路由新增按 BitTorrent 协议、目标端口和 TCP/UDP 匹配，Xray 与 sing-box 都能执行；BT 使用内核自带的协议识别。一条路由的各项条件同时满足才命中。中转入口绑定的路由改为对入口自身用户生效。说明见 [docs-custom-routes.md](docs-custom-routes.md#面板路由)。

`v1.21.0` 包含流量待上报存盘、限速热更新、IPv6 `/64` 设备计数、直连出口内网拦截、统计修正、无截断日志轮转和按实例隔离的可信前置服务器 PROXY 头支持。设备快照保留完整公网来源供面板先排除再合并网段；中转域名保留到落地解析。

`v1.20.0`：新增不计入设备数的来源名单，面板下发 flux 等转发机的出口地址后，从这些地址进来的连接不占设备名额，只改名单不重载内核；节点在线人数改为按所有活跃来源计算。配套面板 `1.24.0`、管理端工程 `0.7.0`。本版同时包含升级下载超时后改用 `gh-proxy.org` 重试，校验文件只从 GitHub 下载。

`v1.19.0`：分别经 IPv4、IPv6 向面板回报公网地址，双栈服务器可同时显示两个地址；设备数超限时上报拒绝事件；并包含 `v1.18.0` 的 Xray REALITY 防盗用模式（见 [docs-reality-anti-abuse.md](docs-reality-anti-abuse.md)）。配套面板 `1.23.0`、管理端工程 `0.6.0`。

[YZboard](https://github.com/P0me1oo/YZboard) 的节点程序，支持 `sing-box` / `xray-core` 双核心。

正式版从 `v1.13.1` 起采用 `v主版本.次版本.修订号`，不再添加 fork 后缀；上游基线与固定核心依赖单独记录在 [兼容矩阵](YZ_COMPATIBILITY.md)。历史版本继续用于升级和回滚。

本项目用于学习和研究。

## 功能

- 协议：V2Ray 系列、Trojan、Shadowsocks、Hysteria2、TUIC、AnyTLS
- 同步：WebSocket 推送与 REST 轮询
- 用户管理：限速、设备数与连接数限制、超限事件上报、在线 IP 跟踪与热更新
- 部署模式：节点、机器与独立运行
- 多实例：一个进程绑定多个面板或节点
- 自动防火墙：Linux 上按节点运行状态管理 UFW/firewalld 端口，保留手工规则和共享端口引用
- HY2 端口跳跃：由 Node 管理 nftables/iptables 转发，支持端口列表、范围及其组合；见 [配置说明](docs/firewall-port-hopping.md)
- 中转：Xray、sing-box 均可作为 VLESS/Hysteria2 入口或 Shadowsocks/VLESS 落地，两种内核可以混用；VLESS Encryption 仅用于两端都是 Xray 的链路
- HY2 ECH：`yz.22` 配合面板 `1.12.0` 支持 ECH 前置入口，已验证 sing-box／Mihomo 客户端、中转混淆、密钥轮换和流量累计
- 默认内核：新建使用 sing-box，VLESS 使用 Xray；机器模式按面板节点分别选择，已有配置保持原内核
- 时间校准：为依赖时间戳的 Shadowsocks 2022 链路提供进程内 NTP 校准
- sing-box：以官方 `v1.14.0` 为基线，`yz.21` 配套 `P0me1oo/YZ-sing-box v1.14.0-yz.2`，保留用户和路由热更新、Mieru，并修复中转检测发现的 gRPC 与 SS2022 关闭竞争；固定依赖和验证记录见 [兼容矩阵](YZ_COMPATIBILITY.md)
- 可靠性：`yz.19` 让配置、重载和用户应用失败直接进入停止/失败状态，不自动恢复旧配置；同时保留空用户同步、内核重载和退出流量结算修复，验证范围与 Linux 测试方法见 [修复验证](docs/node-reliability-validation.md)
- 故障隔离：单个节点端口冲突或配置错误只停止自身，其他节点继续服务；整体 `/healthz` 返回 503 表示至少有一项失败，不代表所有节点停止。

## 安装

以下说明适用于 `v1.15.1`。安装包、镜像及固定来源以 GitHub Release 和 [兼容矩阵](YZ_COMPATIBILITY.md) 中的正式发布记录为准。

### Docker

```bash
docker run -d --restart=always --network=host --stop-timeout=150 \
  -e apiHost=https://panel.com -e apiKey=TOKEN -e nodeID=1 -e kernel=singbox \
  ghcr.io/p0me1oo/yz-agent:latest
```

退出时进程最多等待两分钟，完成在途报告、失败批次重试和最后流量上报。Compose 部署应设置 `stop_grace_period: 150s`，避免容器提前被强制结束。

### 安装器（Linux systemd / OpenRC）

安装器会自动识别正在运行的服务管理器。Debian、Ubuntu 等 systemd 系统使用
`yz-agent.service`；Alpine Linux 使用 OpenRC 的 `yz-agent` 服务，并由
`supervise-daemon` 在进程异常退出后自动拉起。

```bash
# 节点模式
curl -fsSL https://github.com/P0me1oo/YZ-Agent/releases/latest/download/install.sh | \
  sudo bash -s -- --mode node --panel https://panel.example.com --token TOKEN --node-id 1 --version latest

# 机器模式
curl -fsSL https://github.com/P0me1oo/YZ-Agent/releases/latest/download/install.sh | \
  sudo bash -s -- --mode machine --panel https://panel.example.com --token TOKEN --machine-id 1 --version latest
```

`v1.13.1` 起，新绑定默认使用 sing-box，VLESS 默认使用 Xray；当前管理入口为 `yz-agent bind add-node/add-machine`。单节点未指定协议时会先读取面板配置，保留面板已选择的内核；旧面板只返回协议时按上述规则选择。查询失败则停止创建配置，也可传入 `--node-type vless` 直接选择 VLESS 默认值。显式 `--kernel xray|singbox` 始终优先，机器模式以面板每个节点的内核选择为准。

已有配置保持原内核；重复安装或绑定时省略 `--kernel` 会保留该实例的内核配置。为兼容历史部署，程序加载没有指定内核的旧配置或环境变量时仍使用 Xray。上面的新建 Docker 示例显式设置 sing-box，VLESS 请改为 `-e kernel=xray`。

### 升级

从 cedar2025 原版或早期 YZ fork 首次迁移到本 fork：

```bash
curl -fsSL https://github.com/P0me1oo/YZ-Agent/releases/latest/download/install.sh | \
  sudo bash -s -- upgrade --version latest
```

日常升级与检查统一使用：

```bash
yz-agent upgrade
yz-agent version
yz-agent service status
```

尚未提供 `yz-agent` 命令的旧安装，使用上面的安装器升级命令。新安装不再单独安装 `xbctl`。

`latest` 只解析 GitHub 最新正式 Release，并固定目标 Tag 后再下载。默认命令与安装器升级都先读取已安装程序的版本；相同版本不下载、不替换、不重启，当前版本更高不自动降级。查询失败或版本无法识别时停止并提示原因。支持带或不带 `v` 的版本号，以及历史两段版本和 `-yz.N` 修订；两段版本补零，修订按数字比较，同基线的无后缀正式版高于 `-yz.N`。需要单独重启时使用 `yz-agent service restart`。

从 `v2.19.3` 起，安装、升级和失败恢复最多等待服务就绪 180 秒，单次本机健康检查最多 2 秒且不经过代理。所有节点就绪后才算成功；服务退出或到期仍未就绪时，继续报错并执行原恢复流程。Linux 下防火墙命令超时会终止本次命令及其子进程，避免遗留命令继续占用输出或修改规则。

旧版本执行 `yz-agent upgrade` 时仍使用旧程序内嵌的安装器。需要从本次健康检查失败中恢复时，先使用新版本 `install.sh` 的 `upgrade` 入口完成一次升级，沿用原配置，无需重新绑定面板或填写令牌；成功后继续使用上面的日常升级命令。

`v2.18.0` 开始，升级命令与安装器均优先直连 GitHub；连接建立最多等待 10 秒，最新版查询总时限 30 秒，附件下载总时限 2 分钟。版本查询和程序下载仅在直连超时后切换镜像，直连的 HTTP 错误保持报错；校验清单获取失败会尝试所有镜像。一次安装或升级中确认直连超时后，后续请求直接探测镜像，避免重复等待。

备用源为 `https://gh-proxy.org/`、`https://ghfast.top/` 和 `https://github.chenc.dev/`。并行请求各源的实际版本接口、校验清单或程序前 4 KiB，每个探测最多 5 秒，检查响应后按 HTTPS 请求耗时排序；不依赖 ICMP ping。程序探测独立于校验清单，即使所有源都没有清单，仍可选源下载。最新版查询只接受非草稿、非预发布的正式版本，并固定 Tag 后再下载。程序下载优先使用耗时最低的可用源，该源请求失败时按顺序尝试下一源；程序下载全部失败时停止，保留原程序。每次新操作重新探测，不永久保存选源结果。

获取到 `SHA256SUMS` 时执行 SHA-256 核验，哈希不匹配立即停止。GitHub 和所有镜像均无法提供有效清单时，自动跳过校验并继续安装或升级，日志明确提示；仍检查程序版本并保留安装失败恢复流程。清单写入失败、缺少目标附件条目或已有哈希不匹配不会触发跳过。清单缺失时依次尝试正式和历史附件名，保留旧版本回滚支持。

清单和程序都经第三方获取时，需要信任镜像；自动跳过校验后，也无法用发布校验值检测文件损坏或篡改。[jsDelivr 的标准路径](https://github.com/jsdelivr/jsdelivr#github)是 `https://cdn.jsdelivr.net/gh/作者/仓库@固定版本/文件`，适用于仓库文件（例如 `install.sh`），不是 GitHub Release 安装包镜像，因此不加入安装包备用源。

以上回退从安装器或新版本程序启动后生效；外层 `curl ... | bash` 的脚本下载由 curl 执行，不会自动继承程序内的镜像选择逻辑。旧版已安装程序也不会因为仓库更新而自动获得此逻辑。

需要回滚或重装固定版本时显式传入旧 Tag，例如
`yz-agent upgrade --version v1.13.1`。回退后会恢复该版本的程序、服务、管理命令与配置目录。

迁移会重新生成标准服务文件，停止等待时间为 150 秒：systemd 使用 `TimeoutStopSec=150s`，OpenRC 使用 `retry="TERM/150/KILL/5"`。自行修改的服务文件会进入安装备份，外部 systemd drop-in 等自定义设置需要另行核对。

### 自定义程序目录（v1.13-yz.23 起）

安装器支持 `--bin-dir /绝对路径`，只安装一个 `yz-agent` 程序（历史版本分别使用 `xboard-node` 和 `xbctl`）。首次安装时在原有安装参数中增加该选项；未指定时，新安装继续使用 `/usr/local/bin`，已有安装沿用保存的目录。

取得对应版本的安装器后，已有安装可以通过升级迁移到其他目录，例如：

```bash
sudo bash ./install.sh upgrade --bin-dir /boot/yz-agent
yz-agent config bin-dir
```

迁移会更新服务启动路径和 `yz-agent` 管理入口，验证新服务后才清理原程序。旧 `/etc/xboard-node` 目录连同实例数据搬到 `/etc/yz-agent`，配置中的受支持文件路径一并更新。OpenRC 日志写入 `/var/log/yz-agent.log`，旧日志保留原位置。程序目录记录在 `/etc/yz-agent/bin-dir`，绑定变更和元数据刷新不会重置它。

后续 `yz-agent upgrade --version <固定版本>` 自动使用保存的目录，无需重复传入路径；`yz-agent uninstall` 和安装器卸载也读取同一记录，仅删除对应程序，不删除用户选择的目录或其中的其他文件。

程序目录必须是可执行、持久化的本地目录，所在文件系统需支持硬链接。路径支持字母、数字和 `/._-`，不能包含空格、特殊字符、重复的中间斜线或 `.`、`..` 路径段。systemd 会等待该目录挂载，OpenRC 会等待本地分区挂载。

升级临时文件直接写到程序分区；旧程序使用硬链接保留，正常升级峰值约为两套程序大小，不再额外复制第三套。下载、校验、替换或启动失败会清理本次临时文件并按阶段恢复原安装；恢复失败则保留恢复文件并报告位置。旧版遗留的 `.new`、`.bak` 文件不自动清理。

自定义目录下不能直接降级到不支持该功能的旧版本。需要降级时，使用支持此选项的安装器先迁回 `/usr/local/bin`。若程序目录位于旧配置目录内部，应先把程序移到独立目录再进行改名迁移。强制结束安装进程后，应先检查遗留锁和恢复文件，再进行下一次安装。实现与验证范围见 [自定义安装目录说明](docs/custom-install-directory.md)。

## 管理命令

`yz-agent` 不带参数显示帮助；`yz-agent run -c PATH` 启动节点，`yz-agent -c PATH` 保留为旧启动参数的兼容入口。

常用命令：

```bash
yz-agent list                          # 查看全部实例
yz-agent status                        # 查看运行状态
yz-agent bind add-node --panel URL --token TOKEN --node-id 1
yz-agent bind add-machine --panel URL --token TOKEN --machine-id 1
yz-agent bind remove-node --panel URL --node-id 1
yz-agent service restart
yz-agent doctor time                   # 检查 NTP 来源与协议时间偏移
```

`yz-agent service status|start|stop|restart|enable|disable|logs` 会使用当前系统的服务管理器。
systemd 日志由 journal 提供；OpenRC 的 `yz-agent` 日志写入 `/var/log/yz-agent.log`。安装、升级、
卸载和节点绑定变更不需要手工改用 `rc-service` 或 `systemctl`。

安装器和 `yz-agent upgrade` 从 `P0me1oo/YZ-Agent` 的同一个 GitHub Release 下载当前架构的统一程序，并使用 `SHA256SUMS` 校验。新版本以 `yz-agent-linux-*` 为正式附件名，同时保留内容相同的 `xboard-node-linux-*` 和独立 `xbctl-linux-*` 迁移附件供旧升级器使用。历史 Tag 和附件保持原名。面板使用的旧 GitHub 下载地址通过仓库重命名重定向到原 Release，不跟随源码分支。

## 配置

兼容历史单面板配置；新增绑定时自动转换为 `instances` 格式，示例见 `config.yml.example`。Docker 配置和数据挂载的容器目标目录使用 `/etc/yz-agent`，宿主机的数据来源保持原有内容。

## 扩展说明

- 自动防火墙和 HY2 端口跳跃：[docs/firewall-port-hopping.md](docs/firewall-port-hopping.md)
- Xray REALITY 最低客户端版本: [docs-xray-reality.md](docs-xray-reality.md)
- Xray REALITY 防盗用模式：[docs-reality-anti-abuse.md](docs-reality-anti-abuse.md)
- VLESS/HY2 前置入口与中转落地：[docs-relay.md](docs-relay.md)
- HY2 ECH 握手、中转与构建验证：[docs/hy2-ech-validation.md](docs/hy2-ech-validation.md)
- 自定义路由：[docs-custom-routes.md](docs-custom-routes.md)
- 自定义出站：[docs-custom-outbounds.md](docs-custom-outbounds.md)
- DNS 服务商与 ACME DNS-01：[docs-dns-providers.md](docs-dns-providers.md)

## 许可证

MPL-2.0.
