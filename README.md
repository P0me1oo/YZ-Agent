# YZ-Agent

本版版本为 `v1.21.0`，配套面板 `1.25.0` 和管理端工程 `0.8.0`；上一已发布版本为 `v1.20.0`。固定依赖和回滚基线见 [兼容矩阵](YZ_COMPATIBILITY.md)。

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

`latest` 只解析 GitHub 最新正式 Release，并固定目标 Tag 后再下载和校验。默认命令与安装器升级都先读取已安装程序的版本；相同版本不下载、不替换、不重启，当前版本更高不自动降级。查询失败或版本无法识别时停止并提示原因。支持带或不带 `v` 的版本号，以及历史两段版本和 `-yz.N` 修订；两段版本补零，修订按数字比较，同基线的无后缀正式版高于 `-yz.N`。需要单独重启时使用 `yz-agent service restart`。

升级程序下载 Release 程序附件时，GitHub 直连和镜像重试各有 2 分钟时限；仅直连超时才切换 `gh-proxy.org`，HTTP 错误不会切换。校验文件只从 GitHub 直连下载，镜像下载的程序仍按 GitHub 上的校验值核验。两次均失败或校验文件下载失败时停止升级，原程序继续运行。

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
