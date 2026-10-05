# YZ-Agent 兼容矩阵

## v2.5.0 Xray 直接接入系统 WG

- 2026-10-05：[Release v2.5.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.5.0) 已发布并核对为最新正式版，固定来源 `a706a22ca0ffc4ef6268a987718b9f870b7c3c4f`。[正式流水线 37273692140](https://github.com/P0me1oo/YZ-Agent/actions/runs/37273692140) 完整 Linux 竞态回归、Mihomo 联调、四组防火墙、安装器、双架构构建、镜像实际版本与 Release 全部通过。
- 12 个附件下载后均匹配 GitHub 大小与 SHA256，11 条校验清单一致；同架构兼容名称与主程序字节一致。四个程序实际构建信息与附件记录一致：上述固定来源、`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64，主程序的两个核心固定依赖及普通 TCP 默认设置正确。
- 正式主程序 SHA256：amd64 `8565636dd5952e35bcf3d03460b8664fca37f7f36e292ed3d5f5996d19a0bee7`；arm64 `134184c593cd9aa78c23f82269cdae357cfeb3967ebae9aef5a8fd515dcfdd83`。
- `ghcr.io/p0me1oo/yz-agent:v2.5.0` 与 `latest` 均为清单 `sha256:7e7d688c41a522dc5f2ab165dab4d02e17cc1a4a5681c53fa23fcb6c1e945275`；amd64 为 `sha256:be5d37cb5a2f56cab2b4974e91636242d9c28d49ea2841f1f3e2212abd803bc2`，arm64 为 `sha256:a571dbd32bc21fdcd44b48d4031c77f3d2f374baeaf3bbd9d951aa70bd9e7cd0`。两个架构的 OCI 来源、版本、配置摘要及全部镜像层可获取性已匿名核验。
- 回滚基线 `v2.4.3`，双架构镜像 `sha256:668c74b45d6600803c31b8d95f58fec41cf3b4d4d7459d25d8df49ef26c5945c` 已再次核验可获取；回滚前先将两端 MTU 调回不大于 1420。
- YT-HK → DGN-HK 跨机新旧对照及 MTU 验证完成，完整数据见 [测速报告](docs/wireguard-benchmark-20261005.md)。新旧约 83–96 Mbps，没有明显提速证据；新方案下载 CPU 时间较低，上传接近。1420 正常，1500 的大流量均超时；sing-box 一次丢 1/100 个 UDP 包，复测 0/100，原失败记录保留。
- 两台服务器的本次进程、程序、日志、临时密钥及专用目录已清理，端口 49183 释放；无命名网络空间及宿主 TUN 残留。YT-HK 原有 yz-agent 进程及既有监听仍在。生产实例未升级；部署时先更新前置、落地 Node，再更新面板，默认继续使用 1420。

- Node 基线 `5cfdf30a8df939384bc12ff666c1d59a7b2f6e44`，保留固定 Xray、sing-box 依赖；本次不修改核心 fork。
- Linux Xray 出站使用原核心 SOCKS 出站和线路计数，创建连接时进入该线路独立网络空间；落地在同一空间监听并直接交给本实例调度器。移除第二个 sing-box 实例和本机代理端口，隧道协议与 `v2.0.0` 以来的系统 TCP WG 相容。
- 配套面板 `1.46.0`、管理端 `0.23.0`；MTU 默认 1420，允许 1280–1500，已有配置不自动覆盖。大于 1420 的值需要前置与落地都更新，1500 不代表性能更高。
- 2026-10-05 YT-HK Linux 实测：四种核心组合、VLESS/HY2 两种入口共八组通过（289.03 秒），覆盖 TCP/UDP、用户增删、重复重载、停用恢复、固定端口改 MTU 及落地重启；两种入口核心的多落地隔离与流量检查通过（1.53 秒）。DGN-HK 的并发网络空间、BBR、TCP/UDP、错误密钥、端口占用回收与恢复四项通过。Windows 模型及 Xray 包完整测试通过。
- 开发过程中测试发现并修正三处接入问题：落地缺失核心上下文、UDP 回包未预留 SOCKS 包头空间、失败拨号返回空连接计数包装时误关闭。未跳过失败路径，以上完整组合在修正后重新通过。

## v2.4.3 VLESS 前置断线探测

- 在首轮配置调整基础上，增加 Node 模块默认运行设置 `multipathtcp=0`，恢复普通 TCP 默认监听，避免 Go 1.27 默认多路径监听拒绝 `TCP_USER_TIMEOUT`。显式多路径设置仍可覆盖默认值；面板、主控协议及两个核心 fork 不变。
- 程序入口和 Makefile 版本同步为 `v2.4.3`，已核对本地及远程标签未占用。单独的 `Linux socket check` 工作流只接受完整提交，用于快速核对监听协议和实际参数，不发布产物，也不替代正式发布的完整回归。
- 发布前 [Linux socket 检查 37262410411](https://github.com/P0me1oo/YZ-Agent/actions/runs/37262410411) 通过，固定验证来源 `036210b8060ec1665937968bca5755c964507a2e`。Go 1.27.1、完整功能标签和竞态检查下，默认监听协议为普通 TCP，实际监听及接入 socket 的等待参数均为 60000 毫秒，保活开关、30 秒首次探测与 10 秒间隔全部通过；显式多路径选择仍可覆盖默认值。
- 修正后的 Windows Xray 包完整测试通过。运行参数及适用范围见 [设备计数](docs/device-counting.md#v243-前置断线探测)。
- 2026-10-05：[Release v2.4.3](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.4.3) 已发布并核对为最新正式版，固定来源 `329053fd3c9b4d00783102ce1a95334492f5f0f2`。[正式流水线 37262566798](https://github.com/P0me1oo/YZ-Agent/actions/runs/37262566798) 全部通过，包含 Linux 完整回归、实际连接参数、双核心与 Mihomo 联调、四组防火墙、双架构构建、镜像及 Release。
- 12 个附件的大小与 GitHub SHA256 全部一致，校验清单的 11 项全部匹配；兼容名称与主程序字节一致。四个程序的实际构建信息与附件记录一致，来源为上述提交、`vcs.modified=false`、`CGO_ENABLED=0`，目标为 Linux amd64/arm64，核心固定依赖正确，主程序包含普通 TCP 默认设置。版本由流水线实际执行 amd64 程序及两个架构镜像核对；不以构建信息是否包含链接参数作为版本依据。
- `ghcr.io/p0me1oo/yz-agent:v2.4.3`、`latest` 和完整来源提交标签均指向 `sha256:668c74b45d6600803c31b8d95f58fec41cf3b4d4d7459d25d8df49ef26c5945c`。两个架构的版本、OCI revision、配置摘要及所有镜像层可获取性已核对。回滚版本 `v2.4.1` 的双架构程序和镜像仍可获取，镜像摘要为 `sha256:0227d1383f88ff3cc11ac4fd4d12622960a189b8cd5743ba5640c044c1207e0f`。
- 本次无需更新面板或核心依赖，未连接生产服务器，也未验证真实手机切网后的释放耗时。升级后仍需观察旧来源是否释放，不承诺固定 60 秒恢复。

## v2.4.2 VLESS 前置断线探测（失败验证，未发布）

- 固定标签来源 `ec49f3ebd555c6731a104b52c15bb6ab0c84305f`，修改基线 `54e827dc16d895653eaccca2c393c847e913462b`。2026-10-05 [正式流水线 37260473483](https://github.com/P0me1oo/YZ-Agent/actions/runs/37260473483) 在 Linux socket 检查失败：实际接入连接的等待参数为 0，预期 60000 毫秒；构建、镜像与 Release 阶段均未执行。标签保留用于审计，未发布此版本。
- [独立定位检查 37261774257](https://github.com/P0me1oo/YZ-Agent/actions/runs/37261774257) 确认配置解析值为 60000，但多路径监听设置参数时报 `protocol not available`，读取监听参数时报 `operation not supported`。修正纳入 `v2.4.3`，未删除或放宽失败的连接参数断言。
- 由 Node 为面向用户的 Xray VLESS TCP 入口设置空闲 30 秒后探测、每 10 秒重试、Linux 未确认数据等待上限 60000 毫秒。正常闲置连接继续保留，关闭时沿用原有设备名额回收和跨节点同步。
- 兼容关系沿用 `v2.4.1`。面板接口、管理端、SS 内部落地与 sing-box 不变，固定依赖仍为 `YZ-sing-box v1.14.0-yz.2` 和 `YZ-Xray-core v0.0.0-20260930033643-7c5728ec7d0f`；不修改两个核心 fork。适用传输和等待时间的限制见 [设备计数](docs/device-counting.md#v243-前置断线探测)。
- 新增配置回归在旧代码上失败，确认 Xray 实际读到的入口参数未开启断线探测。修改后 Go 1.27.0、Windows amd64、完整功能标签下，Xray 包完整测试通过；服务、控制平面和面板通信的设备/实时快照专项通过；双核心的中转授权、重启、重复重载、失败恢复及关闭回收专项通过。
- 新增真实核心联调使用本机回环地址启动 Xray VLESS 前置和 sing-box SS 落地。健康连接闲置 65 秒后仍能通信，重复重载后新旧连接可用，全部关闭后来源与连接数归零，再次连接正常；测试身份仅存在于内存和自动清理的临时目录。
- 首轮 Linux amd64/arm64 完整功能构建通过，构建时的源码标识为上述修改基线、`vcs.modified=true`，只用于本地验证，不作为正式发布产物；文件位于 `D:/codex-tmp/yz-vless-health-20261005/build/`。本机没有 Linux 运行环境，随后正式流水线的 socket 参数检查失败；未将 Windows 测试或交叉构建视为 Linux 参数生效的证明。

## v2.4.1 设备计数与快照续期

- 2026-10-04：[Release v2.4.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.4.1) 已正式发布并核对为最新正式版，固定来源 `488d05c3e43c022e41372cce0c19d78459febf5a`，配套面板 `v1.39.1`。
- [正式流水线 37208479669](https://github.com/P0me1oo/YZ-Agent/actions/runs/37208479669) 全部通过：Linux 完整竞态检查、兼容依赖、安装器、Mihomo 联调、四组原生防火墙、双架构构建、镜像和 Release。
- 12 个附件已下载，全部匹配 GitHub 大小与 SHA256，11 条 `SHA256SUMS` 一致；同架构兼容名称与主程序字节一致。实际程序构建信息与附件记录一致，均为上述固定来源、`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64，两个核心固定依赖与源码一致。流水线实际运行两个架构镜像确认版本与来源。
- 正式主程序 SHA256：amd64 `0e7436ea1d0768132e17bbf0552cce494d722e2c3027c1404ec460a379146b34`；arm64 `5818c22fa13109e49b9e9cd4136e25905407b36e52429b69f9fea947fd32fd29`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.4.1`、`latest` 和完整来源提交标签均为清单 `sha256:0227d1383f88ff3cc11ac4fd4d12622960a189b8cd5743ba5640c044c1207e0f`；amd64 为 `sha256:72edc1fefed0f789ceaefcaee64ff41c2c728e0fff48b10b7f8fdb53ce07ee36`，arm64 为 `sha256:3dfd37450945ceb0bac5989a18b2236ecc1468bfd4b56ad6d31bae4894bb0186`。三个标签、两个架构的 OCI 来源和版本及镜像层已匿名核验。
- 上一版 `v2.4.0` 的 Release 与镜像仍可获取，镜像清单 `sha256:fffe4623b38ba8f3fdb8b00f5e448654a6ec5b56edce0c99a1f39bde1b5654f2`；两个架构及镜像层已匿名核验。未连接或更新服务器；部署时先更新面板，再更新 Node，用户无需强制刷新或重新导入订阅。

- 修改基线 `ce92fc0290387e46584d7c4fdc75263f599466f0`，配套面板修改基线 `abde6eb1dd99e9d64837efb39520245d86ec1f22`。修改前已核对远程标签，`v2.4.1`、面板 `v1.39.1` 未占用。
- Xray 统一实际连接记录，修复设备上限变更后的漏算与关闭残留；记录按用户独立加锁，保持准入与登记连续。三个新增回归在旧代码上失败，包含并发变更后的残留计数。
- 配套面板 `1.39.1` 每十秒续期稳定设备名单，Node 保留相同有效版本续期、旧版本拒绝和三十五秒失联过期。详见 [设备计数](docs/device-counting.md)。
- 管理端和两个核心固定依赖不变，无核心源码、通信格式或流量结算变更；程序入口与构建默认版本均为 `v2.4.1`。
- Go 1.27.0、Windows amd64、完整功能标签下，普通全库及 Xray、服务、控制平面、面板通信和机器管理五包完整竞态检查全部通过。首次普通全库检查在 sing-box 包触发五分钟时限；单独复测 VLESS/Hysteria2 与两种落地核心的四组中转通过。普通全库按仓库既有二十分钟时限复跑通过，sing-box 包用时 `567.732` 秒，包含双核心中转、用户与路由热更新、重载、重启恢复及流量检查。准确命令与环境见 [设备计数](docs/device-counting.md#本地检查命令与环境)。
- Linux amd64/arm64 完整功能构建通过，实际产物均为 `CGO_ENABLED=0`、对应 Linux 架构，固定核心来源为 `YZ-sing-box v1.14.0-yz.2` 和 `YZ-Xray-core v0.0.0-20260930033643-7c5728ec7d0f`。本地构建记录的源码提交为上述 Node 基线，`vcs.modified=true`，包含未提交修复，不作为正式发布来源。程序位于 `D:/codex-tmp/yz-device-resume-20261004/build/`。
- 发布前本地验证程序 SHA256：amd64 `217b907d818b4eb43db91edb5a3fc7ca3711a8c0e29437b5973e2d0aeb6b252f`；arm64 `7ae2ae7f338d598a379b8518bb0225fa0792c2162b6377a505d86eee936d4bba`。本地阶段为 Windows 测试及 Linux 交叉构建，正式 Linux CI 与发布产物以上方记录为准，未执行真实服务器测试。

## v2.4.0 展示状态按需上报

- 开发基线 `a43d54f580f9d1d6aa2c38243aeab4e4bba611a9`，配套面板 `1.38.0`，两个核心固定依赖及管理端不变。
- 根据主控确认中的短租约决定展示采样：有人查看时每秒采样负载和详细指标，无人查看时约每 60 秒采样；用户实时网速仅在用户页面需要时发送。设备、在线、连接统计、核心控制和可靠流量保持原路径。
- 初次连接、旧面板或租约过期退回完整采样；机器声明新能力后可只发送空心跳，仍保留在线与历史记录。先升级面板再升级节点，只升级一端不代表已减少上报。
- 本地完整功能标签下，面板通信、控制平面、服务和机器管理四包普通测试通过；按需采样、共享连接确认隔离、HTTP 回退、错误确认和旧端恢复均有回归。初版测试夹具未初始化连接快照导致失败，补齐采样夹具后通过，未削弱断言。
- 首次并发检查因环境默认 `CGO_ENABLED=0` 未能启动；使用 D 盘现有编译器开启 CGO 后，四个受影响包的竞态检查全部通过。
- 2026-10-02：[Release v2.4.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.4.0) 已发布并核对为最新正式版，固定来源 `138957fc08ae2d2f03ddb5130b6b3fce75a4fc98`。[正式流水线 37022056893](https://github.com/P0me1oo/YZ-Agent/actions/runs/37022056893) 的完整 Linux 竞态、四组原生防火墙、Mihomo 联调、双架构构建、镜像及 Release 全部通过。
- 12 个附件全部匹配 GitHub 大小及 SHA256，11 条 `SHA256SUMS` 一致，同架构兼容名称与主程序字节一致。实际程序构建信息与附件记录一致，均为上述固定来源、`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64，两个核心固定依赖与源码一致。
- 正式主程序 SHA256：amd64 `a865e897b346ff6f0b5a7bca9c36a5d4fcebf9f59d0be077152b7491b01dfde3`；arm64 `3eaec3bc20be621a20b17de25f27898f6e493d952540ae4a405971fd6e5bb616`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.4.0` 与 `latest` 均为 `sha256:fffe4623b38ba8f3fdb8b00f5e448654a6ec5b56edce0c99a1f39bde1b5654f2`；amd64 为 `sha256:854525b7bffafc73758fa82ec4e6faf44724dee651221b705f68e6018df1fb6f`，arm64 为 `sha256:cb3133035361ede49cd09a8c42994980b0387520830b223ac3f908ea678ea3ef`。两个架构的 OCI 来源、版本及全部镜像层匿名可获取均已核验。
- 配套主控已更新到 `1.38.0`，节点尚未部署。只升级主控时整机 CPU 两分钟采样为 56.83%，更新前为 55.87%；节点仍完整上报，不能据此宣称优化收益。待确认节点升级范围后，先单机验证再逐台升级；上一版为 `v2.3.1`。

## v2.3.1 中转线路权限修复

- 2026-10-01：[Release v2.3.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.3.1) 已发布并核对为最新正式版，固定来源 `a78ae6b8b40b6b63d7340d4a86df06631b4989e7`。配套面板 `1.37.1`、管理端 `0.18.0`，保留之前的网速上报、自动端口放行等修改，两个核心固定依赖不变。
- [正式流水线 36871569301](https://github.com/P0me1oo/YZ-Agent/actions/runs/36871569301) 全部通过：Linux 完整竞态测试、兼容依赖、安装器、Mihomo WG 联调、四组原生防火墙、双架构程序与镜像构建。四组 VLESS/HY2 入口撤权测试、旧实例撤权和旧快照保护均在正式 Linux 检查中通过。
- 12 个 Release 附件已下载，全部匹配 GitHub 附件 SHA256，11 条 `SHA256SUMS` 一致；两个架构的兼容名称与主程序字节一致。实际程序内嵌信息与发布构建记录一致，均为上述固定来源、`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64；流水线实际运行两个架构镜像确认版本 `v2.3.1` 与来源。
- 正式主程序 SHA256：amd64 `02662c808301f8d5d9b6e5571241ae4a683ef593d2a4100930b2cc88a081a122`；arm64 `1e7fafd23bb18eba3943ba676f7ad6337d03d0bff6959f4e84c071c9f2bac9a3`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.3.1`、`latest` 和完整来源提交标签均为 `sha256:5118a3539bfbb6bf131f43ba65133c1bc5f219fe100b93cae9d5e8bea67355af`。amd64 为 `sha256:caf9331fcc59917184c3398dd1c5eebaee71e87204c4d3c738b7e65bafed2e36`，arm64 为 `sha256:b7ebb17283d652be9ae4d34f74a0ed7d3db1c6948865c6174eab92c8f9c31556`；匿名获取及两个架构的 OCI 来源、版本均已核验。
- 回滚版 `v2.3.0` 的 Release 及镜像仍可获取，镜像摘要 `sha256:d0f127fe3eaf9da848e0408ab3e708218e1e48e1330dc4872ecc22570311cb02`。先更新 Node、再更新面板；两端配套完成撤权修复，回滚任一端可能恢复旧行为。未连接或更新服务器。

### 发布前本地验证记录

以下保留发布前的本地结果，包括 Windows 扩展检查限制；正式 Linux 发布检查与产物以上方记录为准。

- 修改基线 `38448a6b5e19c3d531fa277cf0c42b709fd21b26`，是上一版正式源码后的发布文档提交，业务源码未变；配套面板 `1.37.1`，管理端仍为 `0.18.0`。已核对远程标签，`v2.3.1` 尚未占用。
- 实现逐用户线路认证与撤权关闭，拒绝旧配置和旧会话越权使用落地。Node 模型、快照摘要、用户同步以及两种核心适配层共同处理线路列表；详情见 [中转线路权限](docs/relay-route-permissions.md)。
- 核心依赖保持 sing-box `v1.14.0-yz.2`、Xray `v0.0.0-20260930033643-7c5728ec7d0f`；无本地核心替换或核心源码变更。
- Xray/sing-box、VLESS/HY2 四组本机真实核心及竞态测试通过，验证旧配置重连、旧会话请求、已有 TCP/UDP 撤权关闭、保留线路、恢复、空权限和重启。补测旧 Xray 实例撤权及重复快照不重启。
- Windows、Go 1.27.0 下执行完整包测试，除 sing-box 测试夹具分配到系统保留的 UDP 端口外，其余包通过。测试节点和回包服务改为同时分配可用 TCP/UDP 端口后，ECH 中转、普通中转、VLESS 传输、路由条件和受影响的 WG 用例全部复测通过；没有跳过或削弱断言。完整功能标签为 `with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api`。
- 竞态检查：`go test -race -p 1 -mod=readonly -tags '<上述完整标签>' ./internal/kernel ./internal/model ./internal/controlplane ./internal/service -count=1 -timeout 10m` 完整通过；两种核心包使用相同选项和 `-run 'TestRelay|TestConnTracker|TestLimitDispatcher|TestXrayUpdateUsers'` 通过。并行链接曾因本机内存不足主动中止，改为串行完成上述检查。
- 扩展竞态检查未通过：`TestHysteria2ECHRelayRuntime/xray/xray/salamander=true` 在现有依赖 `github.com/sagernet/sing v0.9.0-beta.4` 的 `common/bufio/vectorised_windows.go:83` 触发 `checkptr: converted pointer straddles multiple allocations`。未关闭指针检查、修改依赖或将失败记为通过；对应普通模式全部通过。本轮未执行完整 Linux 竞态检查或真实服务器验收。
- 本地 Linux amd64、arm64 完整功能构建通过；实际来源为上述基线加工作区修复（`vcs.modified=true`），版本 `v2.3.1`、`CGO_ENABLED=0`，两种核心依赖均与记录一致。这些程序仅用于开发验证，不作为正式发布附件。SHA256：amd64 `465f0b18acb7b55b02b8a339f079e7e790f940e4f6a8cac65fc15dd395c26073`；arm64 `b2129142522f437c7c65b726da35c711f86bc84691f8754aa9c3de8dbdddf168c`。
- 未发布、未连接或更新服务器。先升级 Node 再升级面板，两端配套才完成修复；旧面板缺省线路字段继续按旧规则工作。

## v2.3.0 移除中转来源限制与用户实时网速

本版纳入下方原计划 `v2.2.0` 的全部网速改动；不单独发布 `v2.2.0`。先升级 Node，再升级面板，可由新版 Node 在旧面板下先完成普通端口放行和旧来源规则清理。

- 2026-10-01：[Release v2.3.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.3.0) 已发布并核对为最新正式版，固定来源 `b55ff8c277409a9cae5cf704287a5bffaeaad374`。配套面板 `1.37.0`、管理端 `0.18.0`；核心依赖保持 sing-box `v1.14.0-yz.2`、Xray `v0.0.0-20260930033643-7c5728ec7d0f`。
- [正式流水线 36861353168](https://github.com/P0me1oo/YZ-Agent/actions/runs/36861353168) 的完整 Linux 竞态检测、兼容包、安装器、Mihomo WG 联调、四组原生防火墙、双架构程序和镜像构建全部通过。四组防火墙测试覆盖 UFW/firewalld 与 nftables/iptables，实际验证旧来源规则恢复普通放行及停止清理。发布前本地服务、模型、面板通信和防火墙四个包回归亦通过。
- 12 个 Release 附件已下载，全部匹配 GitHub 附件 SHA256，11 条 `SHA256SUMS` 一致，同架构兼容名称与主程序字节一致。实际程序内嵌构建信息与发布的构建记录一致，均为上述固定来源、`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64，两个核心固定依赖正确。
- 正式主程序 SHA256：amd64 `69974888ca41bd9738c35e111df087e9b1e4cceb71f1c3d9d8ed977fd898e7c3`；arm64 `9caf608469bfe1e38fbea27342f76f36b66d4f86acdc6dfd5372d22568cf17c0`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.3.0`、`latest` 和完整来源提交标签均为 `sha256:d0f127fe3eaf9da848e0408ab3e708218e1e48e1330dc4872ecc22570311cb02`；amd64 为 `sha256:fb6b2648765742861f3be3bf6c895e1d412274d722234fb824a525f90bb1af94`，arm64 为 `sha256:f79053c5958025ea39719b6dd318483abb083e69ea24aa8beca8b8c0b231320c`。匿名获取与两个架构的 OCI 来源、版本均已核验，流水线实际运行两个架构镜像并确认版本。
- 本地附件核验首次错误假定构建信息会保留链接参数，实际输出不包含这些参数；改为逐项匹配发布构建记录，并核对流水线中程序的实际版本输出后通过。未修改程序或正式测试。
- 回滚基线 `v2.1.0` 的 Release 附件及镜像仍可获取，镜像摘要 `sha256:ee2221870645e670fb0eb6806593421e93c0438d4661c04c71c5913ca6901a88`。回滚会恢复旧来源限制行为，须配套评估面板版本和规则。未连接或更新生产服务器。

### 开发阶段记录

以下保留发布前的本地验证过程；“未发布”等状态只指当时，正式来源与产物以上方记录为准。

面板 `1.37.0`、Node `v2.3.0`、管理端 `0.18.0` 移除自动来源 IP 白名单、出口核对、手动确认和待确认提示，恢复已有自动端口放行。中转绑定、运行开关和端口跳跃不变。

必须配套更新节点程序：只更新面板不能清理旧节点的来源规则。新版 Node 忽略旧面板来源策略；先建立普通放行，再清理本实例旧来源规则，写入失败保留旧规则并重试。保留旧规则格式读取与清理代码，不认领手工或其他实例规则。没有启用 UFW/firewalld 的环境仍不自动安装或启用防火墙。

以下旧版本说明保留为历史记录，不代表新版行为。本次未发布、未操作服务器。

最终本地验证：完整功能标签下服务、模型转换、面板通信、防火墙四个包回归通过；Linux amd64、arm64 构建及 Linux 防火墙测试程序交叉编译通过。固定核心依赖不变，来源为当前提交加原有网速改动与本次删除功能改动（vcs.modified=true）。未执行真实 Linux 防火墙运行验收。配套面板完整回归 431 项、4,682 次断言通过，产物摘要见面板 YZ_COMPATIBILITY.md 本次记录。

## 用户实时网速开发记录（原计划 v2.2.0，已合入 v2.3.0）

- 开发基线 `7187f65f4d2772b5dd82f2506b53b44557b3290f`，配套面板 `1.36.0`、管理端 `0.17.0`。正式核心依赖保持 sing-box `v1.14.0-yz.2`、Xray `v0.0.0-20260930033643-7c5728ec7d0f`。
- 在 Service 中复用累计流量采样，状态新增可选 `user_speeds`，按用户编号对应 `[上传字节/秒, 下载字节/秒]`。省略表示无有效采样，空对象表示全部零速。方向为用户视角，不应用计费倍率，不保存历史。
- 进程启动、采样失败、计数回退或超过五秒采样间隔后重新建立基准。可靠流量批次、补报、连接数和双核心运行保持原路径。
- 新 Node 对旧面板仍走既有兼容路径；完整网速显示需要上述配套版本。当前未发布，未连接真实服务器。
- 本地完整功能标签的服务、面板通信、流量统计包回归通过。Linux amd64、arm64 构建通过，实际产物均为 `CGO_ENABLED=0`，来源为上述基线加本次工作区修改（`vcs.modified=true`），核心固定依赖与前版一致；不是正式发布产物。
- 最终网速专项和两种核心统计专项通过，覆盖真实采样间隔、新用户、空快照、重复时间、失败恢复、计数回退、计费不被网速采样清空，以及双核心停止/启动、重载后的累计流量连续性。命令为 `go test -p 1 -mod=readonly -timeout 5m -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' -run 'TestUserSpeed|TestXrayGetUserTraffic|TestXrayTrafficKeeps|TestConnTrackerRoutedConnectionTracksTraffic|TestConnTrackerPacketCopyPreservesCounters|TestKernelTrafficSurvivesRestart' ./internal/service ./internal/kernel/xray ./internal/kernel/singbox`，三个包均通过；本轮未运行完整 `./...` 或 Linux 竞态检测。
- 本地构建 SHA256：amd64 `66a46652eac53086dc0efe86fc7eb93f1943a33509a4d6b0f418319bd218d20d`，arm64 `a9412878c4f2d5e19fe8d7c845015ace6854b113b6c21716acd03bf5a4237b00`。保留在 `D:/codex-tmp/yz-user-speed/`，仅用于本次验证；本功能尚未真实服务器联调或发布。

## v2.1.0 内置中转来源防火墙

- 开发基线 `574841029367da9fd7e0a999c8b76c74286a4a02`，配套面板 `1.35.0` 和管理端 `0.16.0`。保持当前固定核心依赖和系统 WG 格式。
- 新增落地来源策略、按中转目标回报的系统选路地址，以及待确认/规则失败告警；旧面板未下发来源策略时不会自动为绑定前置的落地新增全开放规则。
- 防火墙专项及服务专项通过，覆盖双栈来源、地址改变、旧全开放规则替换、重复同步、错误保持、运行中新增冲突、分批启动、相邻范围恢复和正常清理后回滚旧格式。Windows 下未执行原生 Linux 防火墙验收或竞态检测。
- Windows 完整功能标签回归使用 `go test -p 1 -mod=readonly -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./...`。除 sing-box 整包触发默认 10 分钟超时外，其余包通过；sing-box 使用相同标签单独加 `-json -timeout 20m` 复测通过，109 项测试、144 个子用例，耗时 641.241 秒。最终包含“无旧规则的待确认落地与普通节点共用端口”保护，防火墙整包回归通过。
- 开发阶段源码的 Linux amd64、arm64 完整功能构建通过，均为 `CGO_ENABLED=0`。实际依赖仍为 sing-box `v1.14.0-yz.2` 和 Xray `v0.0.0-20260930033643-7c5728ec7d0f`；来源为上述开发基线加当时未提交修改，`vcs.modified=true`，不是正式发布产物，也不包含随后原生验收发现的兼容修正。
- 本地产物 SHA256：amd64 `c10f58cf2eec0159c46f5f50f7e54a5f16d576484e65938b761d1f18f5266881`；arm64 `540ee9effebc4dc0c592f0b63ae3b43cbed9be217dd81be888b77ecc06900d00`。
- 发布前原生验收发现 UFW 精确 IPv6 来源不一定带 `(v6)` 标记，以及 firewalld 的 `--get-target`、`--get-ports` 仅支持永久配置。修复来源解析并改查当前运行区域与服务配置；补充回归通过。隔离环境补齐 `/dev/stdin` 等设备后，四组 UFW/firewalld 与 nftables/iptables 的完整原生验收通过，覆盖双栈 TCP/UDP 来源允许与拒绝、旧规则替换、重载与异常退出恢复。上面的摘要仅对应早期本地验证产物。
- 正式标签固定来源 `f687dbfd8fc2c4f2940f9cbb4d0affc2795a405b`；[正式流水线 36792771602](https://github.com/P0me1oo/YZ-Agent/actions/runs/36792771602) 的完整 Linux 竞态检测、兼容包、安装器、Mihomo WG 联调和四组原生防火墙全部通过。此前失败的检查保留记录，不将发布前过程写成一次通过。
- 发布完成（2026-10-01）：[Release v2.1.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.1.0) 已核对为最新正式版；正式流水线的双架构构建、运行版本检查、镜像与 Release 全部通过。12 个附件已下载并核对 GitHub SHA256，11 条 `SHA256SUMS` 全部匹配，同架构兼容名称与主程序字节一致。实际程序与构建记录均为上述固定来源，`vcs.modified=false`、`CGO_ENABLED=0`、Linux amd64/arm64，固定核心依赖不变。
- 正式主程序 SHA256：amd64 `92215b45aa91db6201313e68a4ce9a6c4d6b6dcf8d01629608edcea9c61e04b9`；arm64 `fe8fce81299870627c389e75f75c21191a9e7d459efc67816ee3e1d0cda4de60`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.1.0`、`latest` 和完整来源提交标签均为 manifest `sha256:ee2221870645e670fb0eb6806593421e93c0438d4661c04c71c5913ca6901a88`；amd64 为 `sha256:2c4daacb5d64f1646a153015e822eeec545ed7a97527821b06a05228774d18d0`，arm64 为 `sha256:c36eb7fcab37e958eb5fa1acef093f94446f52578af69071db92ec3979199718`。匿名读取和两个架构的 OCI 来源、版本均已核验。
- 由用户先更新面板 `1.35.0`，再更新前置，等待出口回报并处理待确认来源后更新落地。旧 WG `1.x` 仍需双端迁移，不能与 `2.x` 混用。回滚基线 `v2.0.0` 的 Release 附件仍可获取；回滚前先由新版正常停止并完成来源规则清理。未连接或更新生产服务器。

## v2.0.0 系统 TCP WG 中转

- 修改范围仅为 Node；面板沿用现有 WG 参数，Linux 自动切换系统 TCP。内部连接格式改变，入口和落地必须一起升级，旧版不能混用。
- sing-box 与 Xray 的正式依赖不变；新增的运行层使用独立网络空间、TUN 和现有 wireguard-go 依赖，不修改宿主默认路由。包含下节 `v1.26.2` 误重载修复。
- 双核心八组互通与重载、mihomo TCP／UDP、多落地计费及 DGN-HK 真实 BBR 套接字验证通过，详情和权限要求见 [系统 WG 说明](docs/system-wireguard.md)。跨机速度未作对照，不承诺具体提速幅度。
- 发布完成（2026-10-01）：[Release v2.0.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.0.0) 已核对为最新正式版，固定来源 `5688d6ed596cf9e7be7f49f25b4432390f081b6e`。[正式流水线 36771738458](https://github.com/P0me1oo/YZ-Agent/actions/runs/36771738458) 的完整 Linux 竞态、兼容包、安装器、mihomo 联调、双架构构建、镜像运行版本及 Release 全部通过。前置开发流水线的测试已通过，随后取消重复构建，由正式标签流水线完成交付。
- 12 个附件全部下载并通过 GitHub SHA256，11 条 `SHA256SUMS` 全部匹配；同架构兼容名称与主程序字节一致。实际程序及附带构建记录的来源一致，`vcs.modified=false`、`CGO_ENABLED=0`，固定核心依赖不变。主程序 SHA256：amd64 `26d8488fb31a7b33b76fa2143bfeb588ea55d0f1c2baa2c80516738f4640d4aa`，arm64 `f28bbf4df6f1bf36517f78cf4f76e038d6016d12f4c14dc53de47f569ee9a7c2`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.0.0`、`latest` 和完整来源提交标签均为 manifest `sha256:af311ee077023fec093174db195ea5c955fb47636136cfe7d63b29c1172a7b25`；amd64 为 `sha256:e1ebbd058e37532812d7e12f5574d80ea1fc3d6a3146091c2cf54ff21ccba7dd`，arm64 为 `sha256:0188e84c581fb9993e514c1f4b20f949ca80352edfc1447191f8320498b505a6`。匿名读取、两个架构的 OCI 来源与版本均已核验。
- 升级由用户在服务器执行：先落地、再入口，两端均运行 `yz-agent upgrade`，然后用 `yz-agent version`、`yz-agent service status` 确认。两端版本不一致期间 WG 不可用；回滚基线 `v1.26.2` 也必须两端同步回滚。不需要更新面板或重建节点；未代用户更新生产服务器。

## v1.26.2 配置误重载修复

- 紧急修复基于已发布 `v1.26.1` 的运行代码，只修复机器模式配置复制时空列表变成空值的问题，避免相同配置反复触发核心重建；不包含上节系统 WG 改造。修复源码与回归测试已同步回当前开发目录。
- 配套面板 `1.33.2`、管理端 `0.14.0`；已有 `1.33.0`／`1.33.1` 面板无需为此修复升级。协议和数据结构保持兼容，Xray 仍固定为 `v26.9.3` / `7c5728ec7d0f6deb2facac71f4187bbd76acbb6f`，sing-box 仍为 `v1.14.0-yz.2`。
- 原代码本地复现：同一配置与用户经过三轮实时推送、HTTP 拉取，触发六次重载；修复后为零，真正修改端口仍只重载一次。两台机器的只读日志在同一完整小时各出现 24 次 Xray 核心重建，服务进程没有自动重启。详见 [排查与修复说明](docs/realtime-snapshot-disconnect.md)。
- 本地 Windows Go 1.27.0 正式功能标签全量测试 22 个测试包通过。Git Bash 的安装路径和名称迁移测试失败于符号链接的 `readlink` 断言，相关源码未修改；同版本 Linux 发布流程全部通过，包括 20 个安装目录场景、68 个名称迁移场景。
- 发布完成（2026-10-01）：[Release v1.26.2](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.26.2) 当时为最新正式版，固定来源 `7bafc1381d66d8c3c114cbbf73a5cb1bdb953c79`。[工作流 36762012566](https://github.com/P0me1oo/YZ-Agent/actions/runs/36762012566) 的完整 Linux 竞态检测、兼容包、安装器检查、双架构构建、镜像运行版本校验和 Release 全部通过。
- 12 个附件全部下载并与 GitHub SHA256 匹配，11 条 `SHA256SUMS` 全部通过，同架构程序与兼容名称文件字节一致。四份程序的实际来源均为上述完整提交，`vcs.modified=false`、`CGO_ENABLED=0`，目标为 Linux amd64／arm64，固定核心依赖及 `with_gvisor` 已核对。主程序 SHA256：amd64 `d7c0cd0eaa8ea0a8bbd50f2ce6bf7493adbf2d477f7516dea12ed23ebcd81c83`，arm64 `3d064d83452bf6c0fdb5d80340414a249bb5ae7d47021b7e896f68c096b583dc`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.26.2`、`latest` 与完整来源提交标签均为 manifest `sha256:a56ccd1d2cb4f6eee1d5f8829622f7db043e7766c78354b514d6342cc5e540a6`；amd64 为 `sha256:d108fe584017ec7bce241c3cbd0b8037be949253b3bdf9860afd12fda635d085`，arm64 为 `sha256:5314cc8beaf2d06fd0ac006c5a54db0e057bef267dfc8c9e7bc960f86e86aba7`。匿名镜像读取、两个架构的 OCI 来源和版本均已核验。
- 前置与落地 Node 均需更新，建议先落地、再前置；升级重启会中断已有连接。回滚版本 `v1.26.1` 保持 WG 格式兼容，但仍包含本次修复的故障。未更新生产服务器，修复后的实际 SSH 会话需部署后观察。

## v1.26.1 WireGuard 中转

- 配套面板 `1.33.0`、管理端 `0.14.0`；新增 WG 中转落地，VLESS／HY2 前置与两种核心的四种组合均已验证。
- Xray 固定为 `v26.9.3` / `7c5728ec7d0f6deb2facac71f4187bbd76acbb6f`，Go 模块引用 `v0.0.0-20260930033643-7c5728ec7d0f`；sing-box 仍为 `v1.14.0-yz.2`。新增 `with_gvisor` 构建条件，gVisor 固定为 `github.com/sagernet/gvisor v0.0.0-20260727.0-sing-box-mod.1`，匹配 sing-tun 的用户态栈接口。
- 本地八种组合（VLESS／HY2 × 两种前置核心 × 两种落地核心）通过 TCP／UDP、重复重载、撤销用户、停用恢复与落地重启验证；多落地选路和流量统计通过。真实 Mihomo 1.19.31、sing-box 客户端经 Xray VLESS + Reality + TCP → sing-box WG 的 TCP／UDP 测试通过，目标为本地测试服务。
- 带正式功能标签的全量测试中，其余包通过，服务包的 `TestShutdownSavesTrafficWhenPanelUnavailable` 一次因退出时发送次数不足失败；未修改该测试或服务实现，随后整个服务包独立重测通过，该测试再连续运行 10 次通过。失败原因尚未确证，不将全量描述为一次通过；本机未运行竞态检测。
- 2026-09-30 经用户明确授权，通过 Netcatty 完成 DGN-HK 前置、YT-HK 落地的真实测试。Mihomo 1.19.31 与完整 sing-box 客户端使用真实面板订阅，八种组合的 HTTPS 出口核对及 UDP DNS 均通过；停用后的十六次旧订阅访问被拒绝，启用后原配置恢复。落地核心切换与重启恢复通过，切回核心的一轮有四条线路初次超时，重试后约 16.5–18.1 秒成功，不承诺无中断。
- 退出后 163 个批次全部处理，69 个含用户流量；用户上传 184,229 字节、下载 428,527 字节，与唯一批次合计一致，落地用户扣费批次及失败任务均为零。早期直接出站接口探测曾失败，完整客户端验证通过；控制面临时隧道曾中断并恢复，不把过程写成一次全通过。两台测试实例、身份、配置、数据和隧道均已清理，未更新生产服务。
- Linux amd64／arm64 交叉构建通过，Go 1.27.0、CGO 关闭、包含 `with_gvisor`；固定双核心依赖已核对。来源为基线 `cba2308a90a22dc6700e6c8c5b4e886bde094e6c` 加本次未提交修改，`vcs.modified=true`，不是 Release。
- 本地测试构建 SHA256：amd64 `242f350aa4cd4df2810a00558644c42759a6d8f4b5162f0e8fc91150021b2ee4`；arm64 `a9181f5b1c7e5d894f67efe434d8b394ae1c533c2c893b0d73558811aa57ccab`。原构建缺少用户态栈，补上构建标签并固定匹配 gVisor 后完成构建，不修改两个核心 fork。

- 发布前复核：Windows、Go 1.27.0 下执行 `go test -mod=readonly -count=1 -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./...`，sing-box 测试包触发默认十分钟超时，其他测试包通过；WG 专项复测通过（204.078 秒），单独完整 sing-box 包在诊断用二十分钟总时限内通过（530.822 秒），未改测试断言、场景时限或正式测试命令。上述结果使用旧 Xray 依赖。
- 首次 Linux 全量竞态检测（[运行 36660695515](https://github.com/P0me1oo/YZ-Agent/actions/runs/36660695515)）发现 Xray WG 设备事件线程读取监听回调与初始化写入同时发生，VLESS、HY2 前置均能触发。核心补丁使用监听打开时的同一把锁保证回调初始化完成；核心新回归的十轮 Linux 竞态检测通过。Node 已固定修复提交，完整竞态与发布构建结果在完成后追加。
- 第二次竞态检测（[v1.26.0 运行 36664060367](https://github.com/P0me1oo/YZ-Agent/actions/runs/36664060367)）发现 Xray 落地设备先收包、后安装转发处理函数，网络栈读写冲突；前一处监听回调竞争未再报告。核心 `v26.9.3` 将转发初始化放到设备构造之前，新首包回归在旧代码稳定失败、修复后连续十次通过。`v1.26.0` 没有生成 Release 或镜像，后续使用新标签 `v1.26.1`。
- 发布完成（2026-09-30）：[Release v1.26.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.26.1) 来源为 `1ab30205c95cca8d89cedd9e597d819eb73851c8`，已核对为 GitHub 最新正式 Release。[标签工作流 36665531773](https://github.com/P0me1oo/YZ-Agent/actions/runs/36665531773) 的完整 Linux 竞态、兼容包及安装器测试、双架构构建、镜像运行版本检查和 Release 全部通过。
- 12 个附件全部下载并与 GitHub SHA256 匹配，11 条 `SHA256SUMS` 全部通过，同架构 `yz-agent` 与兼容名称程序字节一致。四份程序的实际构建来源均为上述完整提交，`vcs.modified=false`、`CGO_ENABLED=0`，目标为 Linux amd64／arm64；主程序包含 `with_gvisor` 及本节列出的固定双核心、gVisor 依赖。主程序 SHA256：amd64 `1d1cdea1b93cdf49199c95ff38c905f7112448598e72bf4c592ee30d20080303`，arm64 `fd0c88551152109ab541d0995fe37f3de10b9766f1bd00ebe45e3464639957c5`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.26.1`、`latest` 与完整来源提交标签均为 manifest `sha256:4e85d61ecdf74658d058ecb7a3fafab853dcf708dd753d3a12f24e58f64d7ccf`，amd64 为 `sha256:7b41b07297e53b01124eee7cf73f8d983c2c1545e0427c72171dfcea96768f2e`，arm64 为 `sha256:944cb42f069bc8c43cf2f2d20ab1fb77592c58164f65f2f7faef27e86749ae49`。匿名获取、两架构 OCI 版本和来源提交均已核对。
- 回滚基线为 Node `v1.25.1`、面板 `1.32.0`；回滚前停用新增 WG 线路，保留待上报流量批次。跨机验收使用修复前的工作区程序，最终修复通过上述完整竞态与构建验证；没有再次连接或更新生产服务器。

本文件记录可发布的 Node 构建与内嵌内核之间的固定关系。构建上线时必须使用明确的 Node Release Tag 和固定的 Xray fork commit，不能依赖 `main` 或其他移动分支。

## v1.25.1 实时通信与发布回归修正

- 配套面板 `1.32.0`、管理端 `0.13.0`。运行逻辑与下节已完成真实联调的 v1.25.0 测试程序一致，核心依赖不变；本次修正命令入口测试并递增版本。
- v1.25.0 云端测试在两个版本命令用例失败，原因是断言仍要求 v1.24.0，并非运行时故障。该流程未执行构建和发布，没有 Release；旧标签保留。修正后同时核对版本、构建时间和提交信息，未删除或跳过用例。
- 修正后的正式功能标签命令包回归通过：`go test -mod=readonly -count=1 -tags 'with_quic with_utls with_wireguard with_acme with_clash_api' ./cmd/yz-agent`。最终来源的云端完整并发检测与构建检查也已通过。
- 发布核验（2026-09-30）：来源提交 `8627e6d578f50864c792c9d18fa2305b9f81fe0a` 的 [Release v1.25.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.25.1) 已发布，且为 GitHub 最新 Release。12 个附件全部下载并与 GitHub digest 匹配，其中 11 个文件与 `SHA256SUMS` 匹配，同架构 `yz-agent` 和兼容名称 `xboard-node` 字节一致。四份程序的实际构建信息均为该提交、`vcs.modified=false`、`CGO_ENABLED=0` 和对应 Linux 架构；固定核心仍为 YZ-sing-box `v1.14.0-yz.2`、YZ-Xray-core `v0.0.0-20260926140203-434e1c025c93`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.25.1`、来源提交标签和 `latest` 均为 manifest `sha256:a6e35dff6efb3833dd83e3dd67f8f75c3453820a7214be42bac87adc09c42451`，包含 `linux/amd64`（`sha256:4207716eacdb7f289523b134c7d0891729b9cf92e1b95557484fc315cad9e221`）和 `linux/arm64`（`sha256:90de014380a3fb30dfd525ba91999ef218c1a1f3bed87f6321edf835ef728304`）。两种架构的 OCI 来源提交与版本均已匿名核对；云端完整并发检测、双架构构建、镜像运行版本校验和 Release 全部通过。未更新生产服务器。
- 回滚基线仍为 v1.24.0，保留待上报批次。

## v1.25.0 实时通信（未发布，并入 v1.25.1）

- 修改基线 `381dc0c8a352c7e93d3036e8c8bb9be98447c360`，配套面板 `1.31.0` 和管理端 `0.12.0`。核心依赖沿用 `v1.24.0` 的固定版本，两个核心 fork 没有修改。
- 节点状态独立每秒采样，空快照清空，未知状态不推断为零；长连接认证与基线同步完成后切回，HTTP 默认十秒兜底。流量沿用原落盘批次、确认和重试周期，跨通道保持原编号。
- 配置、用户、设备使用有版本的完整快照，拒绝旧 HTTP 覆盖新推送。机器模式通过当前共享连接发送，支持后续发现 WebSocket。
- 旧面板未声明新协议时继续原 HTTP 报告。回滚基线为 Node `v1.24.0` 和面板 `1.29.0`；保留未确认批次文件，不能通过删除待上报流量完成回滚。
- 本地正式功能标签全量测试（22 个测试包）、机器连接专项及 `go vet` 通过；Linux amd64、arm64 测试程序构建和固定核心来源核验通过，测试构建标记为 `vcs.modified=true`，不是正式 Release。
- 2026-09-30 真实 Linux/Redis 联调通过：两种核心分别收发、机器共享连接同时管理双核心、WebSocket 断开和静默后的 HTTP 回退、空机器后续绑定、后续启用连接、封禁清空、重载与离线重启补报均已核对。丢确认后约五秒用 HTTP 重发原批次，最终九个非空唯一批次与用户累计一致：上传 4,980,736 字节、下载 3,932,160 字节。测试服务与实例数据已清理，详细过程见配套面板 `docs/realtime-acceptance.md`。
- 真实请求失败发现错误日志含认证查询参数，已修复请求错误包装并保留取消错误识别；定向测试和双架构重新构建通过，修正版在真实连接拒绝时未输出测试令牌。

## v1.24.0 真实连接数上报

- 配套面板 `1.29.0`、SubscriptionHub `4.51.0`；固定 Xray 与 sing-box 依赖不变，核心 fork 不修改。上一发布版本为 `v1.23.0`。
- 节点报告新增每用户真实连接数和按实际出网节点拆分的连接数。旧面板忽略字段；新面板配旧节点时没有真实连接样本。原有在线来源、设备数限制、流量结算保持原口径。
- 本地验证：`go build ./...` 通过；Xray、sing-box 同一 IP 多连接与关闭回收的定向测试通过，节点报告序列化和服务快照测试通过；服务、跟踪器、控制面与命令包测试通过。未连接真实服务器。
- 发布核验（2026-09-28）：来源提交 `c5a157480e2392b9c71877bbc12102f8f01bbf0c` 的 [Release v1.24.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.24.0) 已发布，且为 GitHub 当前最新 Release。12 个附件全部下载并与 GitHub 摘要匹配；其中 11 个文件与 `SHA256SUMS` 匹配，同架构的 `yz-agent` 和 `xboard-node` 字节一致。四份构建信息均为该提交且 `vcs.modified=false`，目标为 `linux/amd64` 或 `linux/arm64`，`CGO_ENABLED=0`；固定核心依赖为 YZ-Xray-core `v0.0.0-20260926140203-434e1c025c93`、YZ-sing-box `v1.14.0-yz.2`。镜像 `ghcr.io/p0me1oo/yz-agent:v1.24.0`、`latest` 和来源提交标签均为 manifest `sha256:704999dd4275718f53db0f3d57200eef3644153d9f6d2ab16a9b64e136fcd9ca`，包含 `linux/amd64`（`sha256:7d338184abf048d62938c211a41360a2b2b401e5632bdde48ecbc766e2c8b5ab`）和 `linux/arm64`（`sha256:97c9df6f7e65da9248686c65054404373c6aee6fe65e3a7deeb309f5b48712e4`）；两个架构的 OCI 来源提交和版本标签均已核对。标签工作流的测试、双架构构建、镜像和 Release 全部通过。本机没有 Docker，镜像清单通过 GHCR 接口核对。未连接或更新生产服务器。

## v1.23.0 按实际节点上报在线来源

- 修改起点为已发布的 Node `v1.22.0`（`9217d00`），配套面板 `1.28.0`、SubscriptionHub `4.50.1`。核心固定依赖不变：Xray `v0.0.0-20260926140203-434e1c025c93`，sing-box `v1.14.0-yz.2`；两个核心 fork 没有改动。
- 节点报告新增可选字段 `relay_user_alive`，只在中转入口且有在线连接时出现。旧面板忽略该字段；新面板配旧 Node 时收不到拆分数据，连接统计仍按整入口记录。
- Xray 在调度器中按连接携带的线路编号登记出网节点（与 `user>>>…>>>relay` 流量计数使用同一编号），sing-box 在连接跟踪器中按实际选中的出站登记；两者都在连接关闭时回收，重复关闭不重复扣减。排空中的旧 Xray 实例继续参与合并。
- 本地验证（Windows/amd64）：`go build ./...`、受影响包的 `go vet` 通过；`internal/kernel/xray`、`internal/kernel/singbox`、`internal/tracker`、`internal/service`、`internal/panel`、`internal/controlplane` 测试通过。新增用例覆盖两种内核按落地与直连拆分来源、关闭回收、重复关闭，以及普通节点不登记。本机没有 cgo，未运行竞态检测。未连接真实服务器。
- 发布核验（2026-09-28）：来源提交 `ae4fce8f36928ec9919eae47e46d14ab3d08a3a9` 的 [Release v1.23.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.23.0) 已发布，且为 GitHub 当前最新 Release。12 个附件全部下载；11 个文件通过 `SHA256SUMS`，12 个附件又与 GitHub 摘要一致；`yz-agent` 与 `xboard-node` 同架构附件字节一致。四份构建信息均为该提交且 `vcs.modified=false`，Go `1.27.1`、`CGO_ENABLED=0`，主模块版本 `v1.23.0`，核心依赖为 YZ-Xray-core `v0.0.0-20260926140203-434e1c025c93`、YZ-sing-box `v1.14.0-yz.2`。镜像 `ghcr.io/p0me1oo/yz-agent:v1.23.0`、`latest` 和来源提交标签均为 `sha256:da04cbc3eb40220d4bbd4dfa3620cb9f310c031e10460e0d2f4391cccda2caad`，包含 `linux/amd64`（`sha256:9ccb6b5f3e6e17f3422c491df030e4c6693080e4a4613a50a7efa843ff0a80bd`）和 `linux/arm64`（`sha256:b78b2d23bb0d1cd84d6336e869a59e8b515ac7d9a4d038281ecfc6ff59d78192`）；两个架构的 OCI 来源提交和版本标签均已核对。标签工作流的测试、两个架构构建、镜像和 Release 均通过。本机没有 docker，镜像清单通过 GHCR 接口核对。未连接真实服务器。
- 回滚基线为 Node `v1.22.0`、面板 `1.27.1`；回滚后插件的连接统计退回按整入口记录。

## v1.22.0 路由条件与 BT 识别

- 修改起点为已发布的 Node `v1.21.0`（`5e05939e1c81c25b849b59101e83cf43e7285101`，发布记录提交 `5039d6b`），配套面板 `1.26.0`、管理端工程 `0.9.0`。核心固定依赖不变：Xray `v0.0.0-20260926140203-434e1c025c93`，sing-box `v1.14.0-yz.2`；两个核心 fork 没有改动。
- 面板路由新增可选字段 `protocol`（目前只有 `bittorrent`）、`port`（逗号分隔的端口或范围）和 `network`（`tcp`/`udp`），与目标地址同时满足才命中。旧面板不下发这些字段，生成的规则和配置哈希与 `v1.21.0` 相同。
- 兼容风险：旧 Node 不认识这些字段。只填新条件的路由会被旧 Node 整条跳过；同时填了目标地址的路由会被旧 Node 当成“只按地址匹配”，范围变大。先升级全部 Node，再在面板里使用新条件。
- BT 识别使用两个核心自带的嗅探：Xray 在入站开启嗅探、不改写目标；sing-box 使用 `sniff` 动作并只启用 BT 识别器。只有绑定了协议条件的节点开启。
- 中转入口自身线路的直连规则移到面板路由之后，入口绑定的路由对入口自身用户生效；逻辑节点仍在面板路由之前选择落地。
- 本地验证（Windows/amd64，Go 1.27.0，带发布构建标签）：`go vet ./...` 无告警，`go test ./...` 中 22 个有测试的包全部通过。新增用例覆盖条件规范化与拒绝未知值、旧版路由序列化不变、两种内核的规则生成与自带解析器校验，以及两种内核真实收发：Shadowsocks 与 VLESS（UDP 走 XUDP）下 BT 握手和 uTP 建连包被拦截、普通流量放行，“端口 6881 + 仅 UDP”只拦 UDP，中转入口自身用户的 BT 被拦截而经入口到落地的 BT 流量放行。本机没有 C 编译器，`-race` 由标签工作流执行。未连接真实服务器。
- Linux/amd64 和 Linux/arm64 均已用发布功能选项完成本地交叉构建；构建信息确认目标架构及固定的 Xray、sing-box fork 依赖。工作区未提交，构建信息中的 `vcs.modified=true` 属于预期状态；本地验证文件已清理，正式附件由标签工作流生成。
- 发布核验（2026-09-27）：来源提交 `b3364ed54164e00f6a3b61296243b50dd56a28f6` 的 [Release v1.22.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.22.0) 已发布。12 个附件全部下载；11 个文件通过 `SHA256SUMS`，全部附件又与 GitHub 摘要一致；两个架构的构建信息均为该提交且 `vcs.modified=false`。镜像 `ghcr.io/p0me1oo/yz-agent:v1.22.0` 与 `latest` 均为 `sha256:997c70765c60bc1eee50d8cf628721554d29321365ae8ac1549aeecd401fe20c`，包含 `linux/amd64`（`sha256:e087f63f87aa827fa8191295777dd6fa70b2d30614b81d78b8408a21de027a7f`）和 `linux/arm64`（`sha256:60f16ee153259e42b25ee46111b92f0588c1625d15e1161ab5837df3f956bee0`）；两个架构的 OCI 来源提交及版本标签均已核对。不可变的来源提交标签对应镜像摘要 `sha256:a6218dc61ea93b911ff41c408b7ed51402eefdd7a4022e33af3e5e8a4f976443`。标签工作流的测试、两个架构构建、镜像和 Release 均通过。未连接真实服务器。
- 回滚基线为 Node `v1.21.0`、面板 `1.25.0`、管理端 `0.8.0`。回滚 Node 前先删除或改回使用新条件的路由，否则会出现上面的兼容风险；回滚后中转入口绑定的路由恢复为对入口自身用户不生效。

## v1.21.0 综合修复

- 修改起点为已发布的 Node `v1.20.0`，配套面板 `1.25.0`、管理端工程 `0.8.0`。Xray 固定为 `v26.9.1` / `434e1c025c93d72310c83bdb3bbcfa23fe4b8317`，Node 模块依赖为 `v0.0.0-20260926140203-434e1c025c93`；sing-box 仍固定为 `v1.14.0-yz.2`。
- 未上报的流量批次和新增累计流量写入节点配置目录，使用临时文件、同步和替换保证文件完整；重启后沿用原报告编号继续上报，面板按编号去重。面板不可用时保留文件，独立运行模式不创建该文件。
- 每个用户持有固定的限速器，套餐调整、开始限速和取消限速会立即作用于已有 TCP/UDP 连接；速度统计按实际采样间隔换算，请求成功只计一次。
- 两个核心的 IPv6 设备计数统一按 `/64` 网段合并，面板设备详情也返回网段地址。sing-box 补上 `0.0.0.0/8` 与 `::/127` 的拦截；Alpine/OpenRC 普通日志文件按大小轮转。
- Node 上报完整公网来源，由面板先按精确地址或网段排除，再合并 IPv6 `/64`；面板下发的跨节点设备键已经过滤，不再被 Node 按原始地址二次排除。两个核心各自保存设备排除名单，不会互相覆盖。
- sing-box 在入口节点只拦截字面内网 IP，先选中转再解析其他域名；本机直连和落地出口在解析后拦截内网地址。按 IP 放行的自定义规则在解析后仍能生效。中转使用原域名，保留落地 DNS 选择。
- Xray 的 PROXY 头可信名单跟随内核实例及其监听，不再是整进程共用。退出补报的 90 秒从等待在途请求开始计，最后请求使用同一截止时间；到期时待确认批次留在磁盘，下次按原编号重发。
- 日志轮转改为旧文件改名后切换实际写入目标；程序文件日志与 Linux 普通文件标准输出、错误输出均不再复制后截断，避免轮转窗口丢日志。
- 配置热重载更换日志输出文件时，轮转任务同步替换检查路径并立即检查新文件；即使启动时未配置文件输出，之后切换到文件也能开始轮转。OpenRC 标准输出与错误输出的固定路径继续纳入检查。
- 日志热重载回归（2026-09-26）：新增测试覆盖启动无文件、切换到文件及再次换文件后旧路径停止轮转；轮转模块和主程序在普通及完整功能标签下的测试均通过。
- `device_ip_exclude` 名单同时作为可信前置服务器名单：Xray 名单内来源可选携带 PROXY 头，名单外来源不解析；名单为空时旧接收开关也不重新信任所有来源。sing-box 不支持接收 PROXY 头，打开设置时记录明确提示。
- 发布核验（2026-09-26）：Node 提交 `5e05939e1c81c25b849b59101e83cf43e7285101` 的 [Release v1.21.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.21.0) 已发布，12 个附件全部下载并通过 `SHA256SUMS` 和 GitHub 附件摘要核对；两个架构的正式程序均为 `vcs.modified=false`，实际模块包含上述固定 Xray 依赖。镜像 `ghcr.io/p0me1oo/yz-agent:v1.21.0` 与 `latest` 均为 `sha256:e8e615c45554a4505c9df32da707bde2726dae1983d47bc2536093982d52298b`，包含 `linux/amd64`（`sha256:2191bf9a4a25fded1ee589e3dc481cfa114398c74ae784ec982f4576eeedf65f`）和 `linux/arm64`（`sha256:c37dcb6fd144eb607b4e8389252827ed96a0192e567e383080331b26ec713e4e`）；镜像版本和来源提交标签已核对。Node 标签工作流的测试、两个架构构建、镜像和 Release 均通过。面板完整 PHP 回归使用 `pdo_sqlite` 通过（313 项、3,697 个断言）；管理端 `npm run verify` 通过（29 项行为测试、33 项浏览器测试和开发模式检查）。未连接真实服务器。
- 回滚基线为 Node `v1.20.0`、面板 `1.24.0`、管理端 `0.7.0`。

## 不计入设备数的来源名单（v1.20.0，已发布）

- 发布来源：Node `v1.20.0` / `2ea9ece0b1ce9e0da044273b487c9d07e27ebf9a`，配套面板 `v1.24.0` / `26c545e34de1f5ae45f487cf1b95ed67b6829351`。Release <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.20.0> 的 12 个附件已下载，`SHA256SUMS` 全部匹配；`yz-agent` 两个架构的构建信息为上述提交、`vcs.modified=false`，核心依赖为 YZ-Xray-core `v0.0.0-20260922231406-f242ad693152`、YZ-sing-box `v1.14.0-yz.2`。镜像 `ghcr.io/p0me1oo/yz-agent:v1.20.0` 与 `latest` 为 `sha256:5fc352f414f522d01b4f8f90a4d88587e5dc031b89520c98114c97868e46d18f`，`linux/amd64`、`linux/arm64` 的 OCI 来源提交一致。标签工作流的测试、构建、镜像和 Release 均通过。
- 修改起点为已发布的 `v1.19.0`（`e642f5966d74adeab6dd59db91d120e1530fdb18`，发布记录提交 `757350f98afea91e53be4788d9109176c997020c`）。核心固定依赖不变：YZ-Xray-core `v0.0.0-20260922231406-f242ad693152`，YZ-sing-box `v1.14.0-yz.2`。配套面板 `1.24.0`、管理端工程 `0.7.0`。
- 节点配置新增可选字段 `device_ip_exclude`（IP 或网段列表，对所有节点相同）。该字段不参与配置变化判断，只改名单时不重载内核；WebSocket 推送、定时拉取和启动时都先应用名单。字段缺失按空名单处理，旧面板下行为与 `v1.19.0` 相同。
- 两种核心登记所有来源地址，设备准入和设备上报只计“公网且不在名单内”的来源，名单变化对已有连接立即生效。设备超限事件的已计入来源数按同一口径统计。
- 节点上报的在线人数按全部活跃来源计算，修复 `v1.17.3` 起只经内网或本机转发连接的用户不计入节点在线人数的问题。
- 本地验证（Windows/amd64，Go 1.26.4）：相关包 `go vet` 通过；带发布构建标签的 `go test ./...` 21 个包全部通过，新增名单解析、tracker 上报、Xray 与 sing-box 准入、服务层不重载共 5 项；`linux/amd64`、`linux/arm64` 交叉构建通过。开发构建的 VCS revision 为修改起点 `757350f`，`vcs.modified=true`，不可作为正式发布产物。本机没有 C 编译器，未运行 `-race` 并发检查；未连接任何服务器，未做真实 flux 转发联调。
- 回滚基线为 `v1.19.0`。回滚后面板下发的名单被忽略，转发机地址重新计为一台设备；节点在线人数恢复为只按公网来源统计。
- 本版合入尚未发布的 `v1.19.1`（下节）：升级下载超时后改用镜像重试，校验文件只从 GitHub 下载。

## Release 下载超时切换（v1.19.1，未发布，并入 v1.20.0）

- `yz-agent upgrade` 仍先查询并固定最新正式版本；下载该版本的程序附件时，GitHub 直连单次 2 分钟超时后，按 `https://gh-proxy.org/` 加原始链接重试一次。镜像请求同样限时 2 分钟；HTTP 错误及文件校验失败不触发切换。校验文件 `SHA256SUMS` 只从 GitHub 直连下载，避免镜像同时提供程序和校验值，直连超时即停止升级。
- 保留同一 Release 的 SHA256 校验和现有安装事务。直连超时的部分文件在镜像重试前删除；两次失败时原程序不被替换。
- 配套面板 `1.23.1`（并入 `1.24.0`） 的远程任务有效期为 10 分钟；旧面板仍采用原有 15 分钟有效期，两边无协议或核心依赖变化，可分别升级。
- 当前仅为本地修改，尚未发布或进行真实服务器验证。回滚基线为 Node `v1.19.0`。

## 双栈公网地址回报（v1.19.0，已发布）

- 发布来源：Node `v1.19.0` / `e642f5966d74adeab6dd59db91d120e1530fdb18`，配套面板 `v1.23.0` / `85b1441a23a1fad8a195acb367ed4bb9a56725c2`。Node Release 包含 `linux/amd64`、`linux/arm64` 程序和 `SHA256SUMS`；镜像 `ghcr.io/p0me1oo/yz-agent:v1.19.0` 的 manifest 为 `sha256:6e5f72f97fbf417f406bd25e6f03bcbb9db3dbcb9d9d226622e2b361cd455291`，与 `latest` 一致，包含两个目标架构。面板镜像 `ghcr.io/p0me1oo/yzboard:1.23.0-85b1441` 为 `sha256:757369e5f6c1202c7d92520da087859773bd9ce26d2e36644154a1af4987873f`，同样包含两个目标架构。
- 本版一并合入设备数超限上报：Xray 与 sing-box 两种核心在拒绝新公网来源时记录事件，随原有状态上报发送给面板 `1.23.0` 的插件钩子；并发和速率事件格式不变。旧面板忽略新类型事件，核心固定依赖及节点配置不变。
- 修改起点为已发布的 `v1.17.4`（提交 `022aa4c2070d806b1a87503acd4c82cd9b2b7e41`），并合入尚未发布的 `feat/reality-anti-abuse`（`v1.18.0`，提交 `0d2a344588303bc0fc606fa3fbb9ef6223ed3035`）。下节 REALITY 防盗用模式记录的基线、配套和回滚版本以本节为准。核心依赖未改动：Xray 仍固定为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260922231406-f242ad693152`，sing-box 仍为 `github.com/P0me1oo/YZ-sing-box v1.14.0-yz.2`。
- 新增 `internal/panel/machine_address.go`：只经指定地址族（`tcp4` 或 `tcp6`）调用面板 `POST /api/v2/server/machine/address`，沿用已有机器鉴权；与原有面板连接一样不走系统代理、校验证书，单次最长 15 秒，不保留空闲连接。`internal/machine/machine.go` 在启用远程控制时启动回报循环：启动时回报一次，之后每 5 分钟一次，面板返回 404 时降为每小时一次。
- 配套面板 `1.23.0`：面板按来源地址族分别记录公网地址，管理列表 IPv4 在上、IPv6 在下；某一地址族的最近回报比另一种旧 15 分钟以上时不再显示。本版可与旧面板混用，只是不显示第二个地址；旧 agent 配合新面板时，面板只能看到控制请求的那一个地址。
- 本地验证（Windows/amd64）：合并后 `go build ./...` 与 `go test ./...` 全部包通过。新增用例覆盖按指定地址族连接、旧面板 404 与其他错误、面板返回非公网来源、启动时两个地址族各回报一次，以及旧面板下本轮只请求一次后退避；本机 IPv6 回环可用，两个地址族的用例均实际执行。
- 未验证：真实双栈服务器上的实际回报效果；未连接任何服务器。
- 回滚基线为已发布的 `v1.17.4`。回滚后 agent 只发控制请求，面板保留最后记录的地址，另一地址族在 15 分钟后不再显示。

## REALITY 防盗用模式（v1.18.0，未发布，并入 v1.19.0）

- Node 修改起点：`feat/reality-anti-abuse` 分支，基线为 `v1.17.1` 的提交 `a1adc31`。核心依赖未改动：Xray 仍固定为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260922231406-f242ad693152`（fork `v26.8.1`，上游 `v26.9.9`），sing-box 仍为 `v1.14.0-yz.2`。
- 改动范围仅限 Xray 配置生成：新增 `internal/kernel/xray/reality_guard.go`，`buildRouting` 增加防盗用规则参数并把这些规则排在所有面板规则之前。未修改内核、面板通信协议、节点配置格式和流量口径，无数据库迁移。
- 面板下发字段为 `tls_settings.anti_abuse`（布尔，默认 `false`）。开关关闭或未下发时生成的配置与 `v1.17.1` 完全一致；旧面板不下发该字段，Node 行为不变，可与旧面板混用。
- 配套面板 `1.22.0`、管理前端 `0.5.0`。面板需要先升级才能在管理端看到并保存这个开关；只升级 Node 不会自动开启任何节点的防盗用模式。
- 本地验证（Windows/amd64、Go 1.27.1）：`go build ./...` 与 `go test ./...` 全部包通过，其中 `internal/kernel/xray` 新增 8 项防盗用用例，覆盖回源改写、专用入口形状、两条路由规则的顺序、四种不跳转场景、开关取值兼容、域名规范化、端口解析和本机端口分配避让；生成的完整配置由 `serial.LoadJSONConfig` 实际解析通过。
- 未在本机覆盖：`-race` 需要 CGO；`install_paths` 与 `install_name_migration` 在 Windows Git Bash 下因 `ln -s` 退化为文件复制而断言失败，与 `v1.17.0` 记录一致，本次未改动安装脚本。两项均由 Ubuntu CI 覆盖，发布前以 CI 结果为准。
- 未在真实节点上验证实际握手效果：本机只验证了生成的配置内容与内核解析，未连接服务器做 REALITY 探测对照。
- 回滚基线为 Node `v1.17.1`（核心 `v26.8.1` / `f242ad6931521c1d56245e3566dcf7bc11b9571c`）。回滚后已开启该开关的节点会恢复成原来的伪装回源行为，面板上的开关值保留但不再生效。

## 代理下载时限（v1.17.4，已发布）

- 来源基线为 `v1.17.3`。Node 下载 Release 附件的单次总时限从 120 秒改为 5 分钟；面板单次远程操作的 15 分钟有效期不变。
- 2026-09-24 线上只读排查：五台机器均在下载 `v1.17.3` 的 `yz-agent-linux-amd64` 附件约 120 秒后失败，服务保持运行。WAP.AC-TW 的完整下载复测在 125 秒内收到 51,478,516 / 74,207,392 字节后超时，支持下载时限不足的判断。
- 旧版代理的内置下载时限不会因新版本发布而改变；首次升级需由用户使用不受旧代理时限约束的安装器路径。未执行生产服务器更新。
- 配套面板仍为 `1.21.2`；核心固定依赖、配置和通信格式不变。来源提交为 `dd24a534745f52b9c8a52617dd3c4f56d1f5b071`，Release 为 <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.17.4>；已发布 12 个附件，包含 Linux amd64/arm64 程序、安装器、校验清单和构建信息。镜像 `ghcr.io/p0me1oo/yz-agent:v1.17.4` 与 `latest` 已更新，发布 manifest digest 为 `sha256:46b1fa23be65190223e4855461ddd00f66d1f60b843408c253a1908aa13251a5`，包含 `linux/amd64` 与 `linux/arm64`。回滚基线为 `v1.17.3`。

## 公网来源设备计数（v1.17.3，已发布）

- 两种核心只将用户连接的公网来源 IP 用于设备准入和设备快照。私有、回环、链路本地、共享地址、文档地址及无效地址不占名额；连接数限制和流量仍照常工作。公网 IPv4、IPv6 分别按规范化地址计数，IPv4 映射地址与对应 IPv4 合并。
- 内网中转场景：入口若看到客户端公网出口，入口计一次；出口若只看到入口服务器的内网地址，出口不再增加设备数。跨节点同一公网来源继续由面板快照去重。面板需配套 `1.21.2`，前端详情工程配套 `0.4.7`；Node 的核心固定依赖与通信格式不变。
- 来源提交 `80e993b2c843d6642946a081b4c5f4f1d16450a8`；Release <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.17.3>，包含安装脚本、校验清单、两个架构的程序及构建信息。镜像 `ghcr.io/p0me1oo/yz-agent:v1.17.3` 与 `latest` 指向 manifest `sha256:bade09a4f6d41cab78e02a3dcddd260de8d1129bfc8758f70a1913a56e947cd9`，包含 `linux/amd64` 与 `linux/arm64`，两个架构的 OCI 来源提交一致。回滚基线为 `v1.17.2`；旧版仍会把内网地址计入设备数。
- 验证：公网地址分类、Xray 与 sing-box 两个内核的完整包测试、服务层、面板通信层和命令行版本检查均通过；正式 CI 的测试、两个架构构建、镜像发布和 Release 通过，下载的构建信息均记录上述来源提交且 `vcs.modified=false`。面板完整 PHP 回归 294 项、3581 个断言通过，未连接真实服务器。

## 在线设备计数与限制（v1.17.2，已发布）

- 面板现有设备记录按节点上报的来源 IP 跨节点去重，保留约 300 秒；Node 本次统一两种核心的准入规则。来源 IP 是节点实际看到的连接上一跳，不是物理设备标识。内网地址可能来自同机转发、容器网络或内网上一跳；仅凭地址类型无法归因，不能统一排除。
- 直连按客户端出口地址计数；链式连接和填写中转 IP 的外部转发按连接目标节点的转发出口地址计数。面板内置中转使用独立落地凭据，只在入口按客户端连接来源计数一次。不同节点看到相同 IP 合并为一个名额，不同 IP 分别占名额。
- sing-box 与 Xray 均使用面板的跨节点快照，快照超过两分钟或控制通道断开时退回本地活跃连接。已有来源继续使用，名额满时新来源被拒。面板显示可能因约 300 秒保留期、上报延迟或已建立连接暂时超过上限；本次不主动踢除已建立连接。跨节点同时建立连接仍可能在快照到达前各自占名额，若要求严格上限，需要面板提供原子预留接口和故障策略。
- SS 密码无法可靠区分共用出口的物理设备。若未来要求按真实设备数限制，需要客户端提供可验证且不能随意伪造的设备身份，并为每台设备分配独立凭据；现有 SS 订阅不能直接满足。此版本没有改变面板字段、Redis 格式或核心 fork，配套现有面板设备快照接口。
- 来源提交 `08a6f0a88be06b4193eb9b0a304fd905b66ec06a`；Release <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.17.2>。镜像 `ghcr.io/p0me1oo/yz-agent:v1.17.2` 与 `latest` 指向 manifest `sha256:a408563040bd5cbca3d349c463ad7235616ef63c5c84844f912bfd998db946df`，包含 `linux/amd64` 与 `linux/arm64`。回滚基线为 `v1.17.0`。
- 本地 Windows/amd64 使用正式依赖执行 `go test ./...`，全部包通过；面板现有设备去重单测 1 项、3 个断言通过。Linux/amd64 开发构建通过，未进行真实服务器连接测试。

## Reality 传统与混合握手兼容（v1.17.1，随 v1.17.2 发布）

- Xray fork 固定为 `v26.8.1` / `f242ad6931521c1d56245e3566dcf7bc11b9571c`，上游仍为 `v26.9.9` / `52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`。正式依赖版本为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260922231406-f242ad693152`，不是本地路径或移动分支。
- 修复目标是让 Xray Reality 入站同时接受传统和混合握手；Node 的面板通信和节点配置格式不变，sing-box fork 版本不变。Mihomo 订阅自动保留混合握手信息由面板 `1.21.0` 提供。
- 核心回环互通测试使用固定 mihomo 和 YZ-sing-box 客户端，通过两种目标站点、重连、混合并发、数据传输和错误认证；实际协商算法另由 Xray 客户端测试断言。Node 的 Xray 配置包在临时本地核心替换下通过；sing-box 运行时包在 120 秒测试上限处超时，不能据此报告 Node 完整回归通过。
- 核心扩大回归中的 Sudoku 端到端测试在内部构建阶段超时，尚未完成该场景。正式发布前还需完整测试、目标架构构建、固定模块解析和构建来源核验，不能沿用 `v1.17.0` 的 CI 结果。
- 当前无新的 Node Release、安装包或镜像，服务器继续使用既有已发布版本。回滚基线为 Node `v1.17.0`，其核心仍要求客户端携带混合握手信息；回滚时需核对所用客户端订阅。

## Xray 内核同步至 v26.9.9（v1.17.0，已发布）

- Node 修改起点：`74a066c3a5ec42417f99a4acfc6ad808f5dfdcc3`；原 Xray 依赖为 `b4caa82d6414196565599c19ebc1b53e331349b6`，基于官方 `v26.7.11`。
- 新核心：`YZ-Xray-core v26.8.0` / `9fcf874e21147978c5117e4832c78b4adadd4320`，上游基线为官方 `v26.9.9` / `52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`。核心侧合并已推送；产品版本改用独立三段语义版本，不再使用 `-yz.N` 后缀。
- 正式 `go.mod` 的 replace 固定为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260922055117-9fcf874e2114`，`go mod verify` 全部通过，临时本地路径未进入正式依赖。`internal/buildinfo` 的上游 Tag、上游 commit、fork 版本和 fork commit 同步更新。
- 保留 YZ 用户统计、用户与落地流量归属、动态限速、SS2022 时间校准、缓冲写入统计、XUDP 与 UDP 关闭兼容、HY2 会话关闭同步补丁；节点配置、面板通信格式和流量口径不变。
- 工具链同步至 Go `1.27.1`（本地、CI、镜像），`go.mod` 的 go 指令提升为 `1.27`。sing-box fork 保持 `v1.14.0-yz.2`，共享间接依赖随 tidy 更新（gRPC、protobuf、quic-go、REALITY、OpenTelemetry 等）。
- 本地验证（Windows/amd64、Go 1.27.1）：正式依赖下 `go test ./...` 的 20 个包全部通过，含双核心中转、配置、控制通道与服务回归；`sing-anytls`、`sing-shadowsocks`、`singbridge`、`hysteria`、`v2raygrpclite` 等依赖包测试通过；`tests/upgrade_version_test.sh`、服务管理和默认内核脚本测试通过；`linux/amd64`、`linux/arm64`、`windows/amd64` 以正式构建参数编译通过。
- 未在本机覆盖：`-race` 需要 CGO；`install_paths` 与 `install_name_migration` 在 Windows Git Bash 下因 `ln -s` 退化为文件复制而断言失败，与 `v1.16.1` 记录一致，本次未改动安装脚本。两项均由 Ubuntu CI 覆盖，发布前以 CI 结果为准。
- 核心侧的补丁清单与验证记录见 YZ-Xray-core 的 `YZ_FORK.md`。
- CI 核验（2026-09-22）：Node 运行 `35720158391`（Tag `v1.17.0`）的测试、双架构构建、镜像和发布作业全部成功，Ubuntu 上的 `-race` 全量测试与安装脚本测试通过，补齐了本机未覆盖的两项。核心 fork 提交 `9fcf874e` 的三平台「Tests and Checkings」及两个构建工作流同样全部成功。
- 发布核对：Tag `v1.17.0`，来源 `806a3db3911fc22967f37ce86c85d866c02ab31d`，Release <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.17.0> 已标记为 latest。12 个附件已下载，`SHA256SUMS` 的 11 项全部匹配。
- 四个程序均为 Go `1.27.1`、对应 Linux 架构、`CGO_ENABLED=0`、五个功能标签，模块版本 `v1.17.0`，`vcs.revision` 为上述来源且 `vcs.modified=false`；附带的 buildinfo 确认 Xray 固定为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260922055117-9fcf874e2114`，sing-box 仍为 `v1.14.0-yz.2`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.17.0` 与 `latest` 指向同一 manifest `sha256:f424d767b24a5b3c56ce586790fc7401b428d3f6aed5347d9918cceed035885b`，包含 `linux/amd64`、`linux/arm64`；OCI 标签的 version 为 `v1.17.0`、revision 为上述来源。
- 回滚基线为 Node `v1.16.1`（核心 `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`）。配套面板无需同步升级。尚未在真实服务器执行线上更新。

## 升级版本检查（v1.16.1，已发布）

- Node 修改升级命令、安装器及机器操作结果上报；配套面板 `1.20.3`、前端 `0.4.3`。核心依赖、节点配置和服务手动重启入口未变。
- 默认升级固定最新正式 Tag，比较当前版本后决定是否进入原有校验及安装事务；显式 `--version` 保留历史回滚行为。
- 控制回报新增可选 `operation.result`：`updated`、`up_to_date`、`current_newer`。面板仅对升级的后两种成功结果允许不换进程完成；实际更新和手动重启仍要求新进程回报。错误码新增 `release_query_failed`、`current_version_failed`、`current_version_invalid`、`latest_version_invalid`。
- 旧面板不能完整处理新结果；部署顺序应先更新面板及前端，再更新 Node。新面板兼容 Node `v1.16.0` 不带结果字段的旧回报；旧 Node 的升级策略不会因更新面板而改变。
- 本地 `go test ./internal/agentcli` 通过，覆盖版本判断、正式版查询、远程执行去重及原有下载校验、回滚；`tests/upgrade_version_test.sh`、服务管理和默认内核脚本测试通过。
- Windows Git Bash 将 `ln -s` 生成为文件副本，目录迁移和名称迁移测试分别在管理入口及 `readlink` 断言失败；未跳过或弱化这些断言，仍需 Linux 环境复验。
- 发布核对：Tag `v1.16.1`，来源 `8443fb3ced88e1811a865de4ff7490ccb8b2129b`，CI `35665787739` 成功；Release <https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.16.1>。
- 12 个 Release 附件已下载，`SHA256SUMS` 全部匹配；GHCR `ghcr.io/p0me1oo/yz-agent:v1.16.1` 与 `latest` 指向 manifest `sha256:2fda8c14719f22bb72cda3ca0440806c6678ad0f5306b642540529223c79e218`，包含 `linux/amd64`、`linux/arm64`，OCI revision 为上述来源。

## 服务器远程管理（v1.16.0，已发布）

- 发布核对（2026-09-21）：固定 Tag `v1.16.0`，来源 `541cc67c1f886225c944f5c2d38091de8df5d872`，CI `35590758071` 全部成功。
- 正式 Release：<https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.16.0>。12 个附件已下载，`SHA256SUMS` 的 11 项全部匹配；双架构程序的实际构建来源一致，`vcs.modified=false`，核心固定依赖未变。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.16.0` 与 `latest` 指向 manifest `sha256:bcda43f7d8697847882f6e21434b4835e8c1b4c63426636fe637563a9ad9f35b`；包含 `linux/amd64`、`linux/arm64`，OCI revision 为上述来源，version 为 `v1.16.0`。
- 配套面板 `1.19.0`；回滚基线为 Node `v1.15.1`、面板 `1.18.1`。未执行线上更新。
- 新功能配套面板 `1.19.0` 和管理前端 `0.4.0`。旧面板返回 404 时原有服务不受影响；旧 Node 需先完成一次常规升级。
- `go.mod`、`go.sum` 和两个核心仓库未改动；双核心共用机器控制通道。
- 当前进程版本和进程标识用于上报，配置重载保留标识，服务重启产生新标识；执行器完成且新进程回报后面板才确认成功。
- 远程操作要求 Linux root 安装，以及 systemd（含 systemd-run）或 OpenRC（含 setsid）。复用已有 CLI 校验和回滚事务，本机全局执行锁避免同机实例并发操作。
- 安装目录 `remote-operations` 只保存任务及固定结果码，不含凭据；15 分钟超时只表示未确认，不强杀安装或回滚事务，执行锁一直保留到子进程结束。解析最新正式版的请求限时 30 秒。
- 已发布，未连接真实服务器。
- 本地 `go test ./...` 通过，包括两个核心、配置、控制通道及服务回归；后续执行器去重、迟到结果隔离及安装锁补充通过 `go test ./internal/agentcli ./internal/panel`。实际 systemd/OpenRC 服务操作尚未在真实服务器验证。
- Linux `amd64`、`arm64` 均以 `-mod=readonly -trimpath -buildvcs=true` 和 `with_quic with_utls with_wireguard with_acme with_clash_api` 构建通过。正式附件的 VCS revision 为发布来源，`vcs.modified=false`；两个固定核心依赖未变。

## 连接限制与超限日志修复（v1.15.1，已发布）

- 发布核对（2026-09-21）：固定 Tag `v1.15.1`，来源 `14450d777c35417645c51a48b28f2ea3b49aa0c0`，CI `35525200829` 全部成功。
- 最新正式 Release：<https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.15.1>。12 个附件已下载，SHA256SUMS 的 11 项全部匹配；四个程序的实际构建来源一致，`vcs.modified=false`，核心固定依赖未变。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v1.15.1` 与 latest 的 digest 均为 `sha256:63873464d1675cc2c9497a4da8b3e1d08e0ca8d60009a5e07893aed2f5a50fec`；linux/amd64、linux/arm64 的 OCI revision 均为上述来源，version 均为 `v1.15.1`。
- 本地 Go 全量测试与带 with_quic 的核心中转、落地监听测试通过。配套面板 `1.18.1`，回滚基线为 Node `v1.15.0`；未执行线上更新。

- 中转修复：在共用服务层隔离落地普通用户全量、增量和轮询同步，防止内部 Shadowsocks 入站不支持用户更新后停止监听；两种核心均适用。配置重载仍更新内部凭据，失败配置不会被用户消息绕过；支持旧面板，建议配套面板 `1.18.1` 的增量推送过滤。与既有连接限制修复共用本修订版本。
| 项目 | 标识 |
| --- | --- |
| 修改基线 | `0435ed7`（`v1.15.0` 发布记录提交） |
| Go 模块 / 分支 | `github.com/P0me1oo/YZ-Agent` / `dev` |
| 修改范围 | 保留 Xray 超限日志提至 Info 的修复；两内核合并并发检查与名额占用，Xray 调度失败一次性回收，周期超限记录快照后清理 |
| 行为变化 | 并发准入不再超发，失败回收不泄漏或重复扣减；超限统计的次数与观测值在同一快照中读取，已删除用户的迟到事件被忽略；观测值指拒绝时已占用名额的最大值，含正在调度的连接 |
| 核心依赖 | 未修改，沿用 `v1.15.0` 的固定 Xray 与 sing-box 依赖 |
| 上报字段与配置 | 未修改，沿用 `v1.15.0` |
| 目标配套面板 | 兼容 YZboard `1.18.0`；`1.18.1` 另行修复后台生成用户及试用套餐的限制同步 |
| 发布信息 | 已发布，详见本节发布核对 |

## 连接数限制（v1.15.0，已发布）

| 项目 | 标识 |
| --- | --- |
| 正式来源 Tag / commit | `v1.15.0` / `fd3fadf55df303afcff043c3ff25bdebda9ccc14` |
| 正式 Release | [v1.15.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.15.0)，2026-09-19 发布，已核对为最新正式 Release；12 个附件：两个架构的 `yz-agent`、同内容的 `xboard-node` 兼容附件、`xbctl` 迁移附件、安装器、四份构建信息和 `SHA256SUMS` |
| 发布 CI | [35446867828](https://github.com/P0me1oo/YZ-Agent/actions/runs/35446867828)：测试、双架构构建、镜像版本校验和 Release 全部通过 |
| 不可变镜像标签 | `ghcr.io/p0me1oo/yz-agent:fd3fadf55df303afcff043c3ff25bdebda9ccc14`；`v1.15.0` 与 `latest` 已核对指向同一镜像，可匿名获取 |
| 镜像索引 | `sha256:22f525717a5afd00e8be87a43e897e5b0682cb31463e07061b4fcff17e4c2f9f` |
| 平台与 OCI 标识 | `linux/amd64`、`linux/arm64`；两架构均为 `revision=fd3fadf55df303afcff043c3ff25bdebda9ccc14`、`version=v1.15.0` |
| 修改基线 | `cf4a51097930b765be3bd64baccec1baa0608c0d` |
| Go 模块 / 分支 | `github.com/P0me1oo/YZ-Agent` / `dev` |
| 修改范围 | 新增每用户并发连接数与每秒新建连接数上限的准入判断，并汇总超限事件随状态上报回传面板 |
| 内核接入 | Xray 和 sing-box 各自在新连接路径上接入；共用 `internal/model` 的 `ConnLimiter` 接口，避免内核包反向依赖 `internal/limiter` |
| 核心依赖 | 未修改。Xray 与 sing-box 沿用原有固定依赖和兼容副本 |
| 新增依赖 | 无。令牌桶沿用已有的直接依赖 `golang.org/x/time v0.15.0` |
| 上报字段 | 状态上报新增可选 `limit_events`，元素为 `{user_id, kind, limit, observed, count}`，`kind` 取 `conn` 或 `rate` |
| 独立部署配置 | `standalone.users[].conn_limit`、`conn_rate_limit`，省略或填 0 表示不限制 |
| 新增指标 | `ConnLimitEvents`，进程启动以来连接数或速率超限被拒的累计次数 |
| 向后兼容 | 面板未下发新字段时用户上限为 0，全部走无锁快速路径，不做计数也不解析用户标识，行为与 `v1.14.0` 一致 |
| 目标配套面板 | YZboard `1.18.0`，已发布。连接数限制与权限组排序同版发布，面板侧的连接数限制记录见该版本条目 |
| 本地验证 | `go test ./...` 全部包通过；覆盖限流器重建与令牌桶调速、内核准入、上报事件快照和面板客户端载荷 |
| 未在本地验证 | 未与真实面板或真实客户端做端到端联调；未在真实流量下核验并发与速率的限速效果 |

本次同时推送了 `dev` 分支和 `v1.15.0` 标签，两次工作流并发运行且都写入 `ghcr.io/p0me1oo/yz-agent:<完整 commit>`，分支那次覆盖了标签那次，使该标签一度指向 `version` 为 commit 字符串的分支构建。`v1.15.0` 与 `latest` 未受影响，重跑标签工作流后三个引用已指向同一镜像。后续发布应先推标签、确认完成后再推分支。

## 当前正式发布（v1.14.0，2026-09-15）

展示名与 GitHub 仓库统一为 `YZ-Agent`，程序、服务与管理命令使用 `yz-agent`，Go 模块为 `github.com/P0me1oo/YZ-Agent`，GHCR 发布目标为 `ghcr.io/p0me1oo/yz-agent`。旧 `/etc/xboard-node` 安装数据迁到 `/etc/yz-agent`；节点通过 `yz-agent run` 启动。面板通信格式及固定双核心依赖保持原有约定。

本次仅发布 Node 仓库的改名与迁移变更；当前安装、升级和回退命令见 [README](README.md#升级)。

| 项目 | 标识 |
| --- | --- |
| 正式来源 Tag / commit | `v1.14.0` / `1bd29cbf23a72c682f25b66beae3ec874b527501`；后续发布记录提交不改变构建来源 |
| Node 上游基线 | `cedar2025/xboard-node` 的 `v1.13` / `0a29338e1f102a462363ce3527417029f89bab28` |
| Release | [v1.14.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v1.14.0)；发布时间 `2026-09-14T17:37:19Z`，对应新加坡时间 2026-09-15；已核对为 GitHub 最新正式 Release |
| 正式发布 CI | [34874770653](https://github.com/P0me1oo/YZ-Agent/actions/runs/34874770653)，测试、双架构构建、镜像与 Release 全部通过 |
| 安装附件 | 两个架构的 `yz-agent`、同内容的 `xboard-node` 兼容附件、`xbctl` 迁移附件、安装器、四份构建信息及 SHA256SUMS，共 12 个 |
| 镜像 | `ghcr.io/p0me1oo/yz-agent:v1.14.0`，支持匿名拉取；完整提交标签与 `latest` 指向同一镜像 |
| 不可变镜像标签 | `ghcr.io/p0me1oo/yz-agent:1bd29cbf23a72c682f25b66beae3ec874b527501` |
| 镜像索引 | `sha256:92b1b6ff60d9e99aa7c36df6cb5d6dcdc115a3566ad1438f3436a6fb24de7aec` |
| 镜像平台与 OCI 标识 | `linux/amd64`、`linux/arm64`；两架构均为 `revision=1bd29cbf23a72c682f25b66beae3ec874b527501`、`version=v1.14.0` |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`；实际 replacement 为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260907200713-b4caa82d6414` |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427`；实际 replacement 为 `github.com/P0me1oo/YZ-sing-box v1.14.0-yz.2` |
| 面板兼容核对 | 只读核对当前面板 `1.15.2` / `c5333caef4d917aa3d9b00d5205a8a7f80ddab30` 的机器安装入口；旧下载地址和原参数继续有效，通信格式不变，不要求配套发布面板 |
| 回滚基线 | Node `v1.13.1` / `ebc52dfd522c140bb03ac34940b46ad77523c58c`；旧镜像 `ghcr.io/p0me1oo/yzboard-node:v1.13.1`，索引 `sha256:6cd65852f0c11a85296660add721f843a6f94d0f7b0ccc81d72e72076638b1eb` |

Linux 正式 CI 完成 `make test`：20 个安装目录场景、68 个名称与目录迁移场景、2 个原目录恢复检查、2 个防火墙回退清理检查及服务模板、默认内核检查全部通过；完整 Go 测试与六个依赖测试包的数据竞争检测通过。发布前修复了部分 Bash 版本在错误回调中沿用原失败状态的问题，目录尚未搬迁时能够按原身份恢复服务。

12 个 Release 附件已下载，实际 SHA256 与 GitHub 附件摘要一致；SHA256SUMS 的 11 项记录全部匹配。两个旧程序名附件与对应的 `yz-agent` 内容一致。四个程序的实际构建信息与附件记录一致，均为 Go `1.26.4`、模块版本 `v1.14.0`、对应 Linux 架构、`CGO_ENABLED=0`、上述来源与 `vcs.modified=false`；两份核心 replacement 及模块校验值匹配固定依赖，`xbctl` 不链接核心。

四个程序内嵌的安装器与正式 `install.sh` 及固定提交中的文件逐字节一致。新旧仓库的 `releases/latest/download/install.sh` 地址都返回本版安装器，SHA256 为 `31131e24ace7bcda452cb1df5cf95e18cddf94a8e8229729e59b094983f665a9`。两个架构的镜像均在 CI 中执行了版本检查；匿名获取的索引、平台清单、配置及 OCI 标识与正式来源一致，入口为 `yz-agent`，默认参数为 `run -c /etc/yz-agent/config.yml`。

| 正式附件 | SHA256 |
| --- | --- |
| `yz-agent-linux-amd64` | `11304b2f3f27be786b68e50f5439cd67eee82fb7941d9ebce3b28576e33e895f` |
| `yz-agent-linux-arm64` | `86377c7642c4be2c1e3fa8df92c163174c154f634b5573b85a2196f7acca86a5` |
| `xboard-node-linux-amd64` | `11304b2f3f27be786b68e50f5439cd67eee82fb7941d9ebce3b28576e33e895f` |
| `xboard-node-linux-arm64` | `86377c7642c4be2c1e3fa8df92c163174c154f634b5573b85a2196f7acca86a5` |
| `xbctl-linux-amd64` | `96ac64b20ea4de689aa3f56b8705702e4b08b72ed0b51e80848388133b212e74` |
| `xbctl-linux-arm64` | `9b99545cc37b59741ace2a363ac1bd9295e8e1494f5665ec7dbe6ed14e430292` |
| `install.sh` | `31131e24ace7bcda452cb1df5cf95e18cddf94a8e8229729e59b094983f665a9` |
| `SHA256SUMS` | `76a3ddb604758d5ca74272669c56038140153edd7e2936ea4e4bb46821b26d35` |
| `yz-agent-linux-amd64.buildinfo.txt` | `7df894896673b1efa557ecafbd48b2aad8ec9d8bae4d9855357255c039eaa293` |
| `yz-agent-linux-arm64.buildinfo.txt` | `bd6882d0d97e190c611454b2b10fa8ea86314b3f69ff46fc455c47a43b779f2b` |
| `xbctl-linux-amd64.buildinfo.txt` | `607bb07dc986b9a0147e816d6f30396552a4a7c7a4a2dffa1ff3d46bd5bc080e` |
| `xbctl-linux-arm64.buildinfo.txt` | `c5e19bbeb778123b12cce11c9770410c5e1bc52207f5c1c497aef614585a9f8e` |

| 镜像平台 | 平台 manifest |
| --- | --- |
| `linux/amd64` | `sha256:334f3d03b2f893d4426a552cb7d301bfa7e802739b940289d7c02ca7fbfdadeb` |
| `linux/arm64` | `sha256:73e7c069a77bf59cdb3c290b3fb52103f4d06b6c47eb22dbf6cf67f8e8841996` |

本次只需更新 Node。仍使用 YZ 旧版 `xbctl` 的服务器先执行两次 `xbctl upgrade`：第一次更新程序和升级器，第二次完成名称与目录迁移；随后使用 `yz-agent version` 和 `yz-agent service status` 检查。已完成改名的安装使用 `yz-agent upgrade` 常规更新。Docker 部署同步使用新镜像地址和 `/etc/yz-agent` 容器内配置挂载路径；服务器操作由用户执行。

## 上一正式发布（v1.13.1，2026-09-10）

| 项目 | 标识 |
| --- | --- |
| Node 正式版本 | `v1.13.1`；Tag、Release、安装包及双架构镜像已发布并核验 |
| 正式来源 Tag / commit | `v1.13.1` / `ebc52dfd522c140bb03ac34940b46ad77523c58c`；后续发布记录提交不改变此构建来源 |
| Node 上游基线 | `cedar2025/xboard-node` 的 `v1.13` / `0a29338e1f102a462363ce3527417029f89bab28` |
| Release | [v1.13.1](https://github.com/P0me1oo/YZboard-Node/releases/tag/v1.13.1)，当次正式版本；发布时间 `2026-09-09T16:56:00Z`，对应新加坡时间 2026-09-10 |
| 正式发布 CI | [34378744231](https://github.com/P0me1oo/YZboard-Node/actions/runs/34378744231)，全部通过；来源为本节固定提交 |
| 正式安装附件 | Node、xbctl 各两个 Linux 架构，安装器、四份构建信息和 SHA256SUMS，共 10 个附件 |
| 正式镜像 | `ghcr.io/p0me1oo/yzboard-node:v1.13.1`；完整提交标签和 `latest` 已核对指向同一镜像 |
| Docker manifest | `sha256:6cd65852f0c11a85296660add721f843a6f94d0f7b0ccc81d72e72076638b1eb` |
| Docker 平台与 OCI 标识 | `linux/amd64`、`linux/arm64`；两架构均为 `revision=ebc52dfd522c140bb03ac34940b46ad77523c58c`、`version=v1.13.1` |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`；实际 replacement 为 `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260907200713-b4caa82d6414` |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427`；实际 replacement 为 `github.com/P0me1oo/YZ-sing-box v1.14.0-yz.2` |
| 配套面板 | [v1.13.3](https://github.com/P0me1oo/YZboard/releases/tag/v1.13.3) / `f88d243b7e7683e183bf0a3d62af1173220b0855`，已正式发布；包含上游同步、默认内核和 64 MiB 插件上传 |
| 配套面板镜像 | `ghcr.io/p0me1oo/yzboard:1.13.3-f88d243`；manifest `sha256:fe398445e0f793c50585f184e1b6babcb80bd33379297676c7d03612b381330e`；版本别名及 `latest` 一致，包含两个 Linux 架构 |
| 上一正式 Node（回滚） | [v1.13-yz.24](https://github.com/P0me1oo/YZboard-Node/releases/tag/v1.13-yz.24) / `af69ef598f4f75b5bfa0509b1e1d01a653378d47` |
| Node 回滚镜像 | `ghcr.io/p0me1oo/yzboard-node:v1.13-yz.24`；manifest `sha256:86c8e0f646ae124fbf44e862bdf3796a5090dd9bb66856826ced8c68662dae24`，两个架构及来源已核对 |
| 面板回滚镜像 | `ghcr.io/p0me1oo/yzboard:1.13.0-2f29916`；来源 `2f2991633d7f9d841165e68e66aff50af494cce3`；manifest `sha256:376fc1668d9a51531098b4d35c0787fe3d4e8e3a10d9b93ca3e720d1cd71cd20` |

Linux 正式 CI 完成 `make test`：安装器服务、默认内核和路径测试，全量 Go 与六个相关依赖包的数据竞争检测均通过；随后完成双架构构建、构建来源核验及两个架构的镜像版本运行检查。amd64 的 Node、xbctl 已执行版本检查，arm64 Node 镜像经 QEMU 执行版本检查。

10 个 Release 附件已下载，实际 SHA256 逐个匹配 GitHub 附件摘要；`SHA256SUMS` 中的 9 项记录均匹配对应附件。安装器内容与固定提交逐字节一致。四个程序的实际 `go version -m` 输出与构建信息附件一致，均为 Go 1.26.4、对应 Linux 架构、模块版本 `v1.13.1`、`CGO_ENABLED=0`、上述来源和 `vcs.modified=false`。Node 的实际核心 replacement 及模块校验值保持固定；xbctl 不链接核心。

| 正式附件 | SHA256 |
| --- | --- |
| `xboard-node-linux-amd64` | `fb41a70ed8b300d16ef7b3e7d6903076ef592e2b0f0e591e88c73256a681c60e` |
| `xboard-node-linux-arm64` | `7eb314ba70caa8262dd59c87d2e32b6a098408b149b12e3346f016ef29574d07` |
| `xbctl-linux-amd64` | `eda43f8449738c6f129a04aa1baf98b45e01ba6647b1701ccc7516664ba87dc1` |
| `xbctl-linux-arm64` | `d2c2009b50217a875b9097a183524ad330748d7feef5af51f3318c341990831f` |
| `install.sh` | `b884dd685a95ec4890a5335c2f742fa8615ef85968cb825d7d1116f3fb5b7491` |
| `SHA256SUMS` | `3a4c35e720b6e661261da38e3008c7995d14cb61d8c721fccbddf81b5b79d407` |
| `xboard-node-linux-amd64.buildinfo.txt` | `f9bf5349e7d6af1411886ef3541ab52e887fd81fc0f0177e6f90e161578ace1c` |
| `xboard-node-linux-arm64.buildinfo.txt` | `9cda7b4b39f6b51c12f923e1e7c9304c0b0354f8328dea2925228d18746ba4ac` |
| `xbctl-linux-amd64.buildinfo.txt` | `734aa1da9c9e0251c4d20340e8359f120d62951dc2f942bec8bdd59d7d223f32` |
| `xbctl-linux-arm64.buildinfo.txt` | `c8e9f5b09d56c892e577f62247fe38ecd2a2204c2dc9818ec2bea2c532952de4` |

通过匿名 GHCR 接口核对版本、完整提交及 `latest` 标签，镜像索引、平台清单和配置的实际 SHA256 全部一致；两个架构的 OCI version 与 revision 均符合本节来源。

| Node 镜像平台 | 平台 manifest |
| --- | --- |
| `linux/amd64` | `sha256:c01154c7cea2db68514a2b8c281eb9e2aaf2491ead9c724671d4c82d0f3ff34a` |
| `linux/arm64` | `sha256:591d67e0a4b9d843e337248af07159c1d4af5c35fb2e9bddb1c690f80c3db8bf` |

服务器更新由用户执行，顺序为 Node `v1.13.1`、面板 `v1.13.3`。常规 Node 升级使用 `sudo xbctl upgrade --version v1.13.1`，再用 `sudo xbctl version` 和 `sudo xbctl service status` 检查；保留上表历史版本作为回滚基线。

## 新建绑定默认内核（v1.13.1，已发布）

| 项目 | 标识 |
| --- | --- |
| 本版 Node 版本 | `v1.13.1`，标准三段正式版本；替代此前未发布的 `v1.13-yz.25` 计划，正式产物见顶部记录 |
| 修改基线 | `af69ef598f4f75b5bfa0509b1e1d01a653378d47` |
| 配套面板 | `1.13.3`；面板新建节点默认 sing-box，VLESS 默认 Xray，并下发明确的 `kernel_type` |
| 安装与绑定 | 未指定 `--kernel` 的新绑定默认 `singbox`，VLESS 默认 `xray`；单节点未指定协议时读取面板配置，面板明确选择的内核优先 |
| 已有实例 | 升级不改配置；重复安装或绑定时，未显式指定内核则保留原内核配置和继承关系；无效的已有配置不被覆盖 |
| 历史省略值 | 配置文件和环境变量部署没有指定内核时继续按 Xray 加载，防止旧部署在升级后切换内核；新建配置由 xbctl 显式写入默认值 |
| 机器模式 | 仍以面板每个节点下发的内核为准，新机器的 sing-box 默认值不会覆盖已有节点下发的 Xray 或 sing-box |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`，未修改 |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427`，未修改 |

单节点新绑定仅提供编号时，使用本次安装凭据读取现有 V1 配置接口，确认协议与面板已选择的内核；不保存接口响应或额外凭据。查询失败、缺少协议或内核无效时停止创建配置。传入 `--node-type vless` 可直接使用 Xray 默认值，也可以通过 `--kernel` 显式选择；已有绑定不因面板暂时无法连接而重新选择内核。

机器模式由配套面板分别选择每个节点的内核。新建 Docker 部署示例显式设置 `kernel=singbox`，VLESS 则设为 `kernel=xray`，旧的环境变量部署不会因升级而改变省略值的含义。

2026-09-09 本地验证使用 Go `1.26.4`、`windows/amd64`。测试覆盖新建、重复安装、旧格式与多实例继承、显式内核覆盖、面板协议发现、错误配置保留，以及机器模式下两种默认值与两种节点内核的组合。

| 验证项 | 结果 |
| --- | --- |
| Go 回归 | `go test -mod=readonly -count=1 -p=1 -tags "with_quic with_utls with_wireguard with_acme with_clash_api" ./...` 全部通过；未启用 `-race` |
| 单节点协议发现 | 补齐自动发现后，重新运行 `go test -mod=readonly -count=1 ./cmd/xbctl ./internal/panel ./internal/config` 通过，覆盖 VLESS 例外、面板显式选择、查询失败和离线重复绑定 |
| 安装器 | Bash 语法检查、`tests/install_kernel_defaults_test.sh` 和 `tests/install_service_manager_test.sh` 通过；安装器测试使用参数替身，不操作真实服务 |
| 面板接口 | 配套面板 116 项测试、1927 个断言通过；V1 仅用节点编号可返回协议和内核，V2 机器发现与配置下发保留已有节点内核 |
| Linux 构建 | Node 与 xbctl 的 `linux/amd64`、`linux/arm64` 四个构建通过，使用 `CGO_ENABLED=0`、`-mod=readonly -trimpath -buildvcs=true`，验证版本为 `v1.13-yz.25-dev` |
| 构建来源 | 四个程序的 `vcs.revision` 均为本节修改基线，`vcs.modified=true`；Node 实际替换模块仍为上表两份固定核心依赖，属于未提交源码的验证产物 |

以上为 2026-09-09 的开发验证记录，原开发构建名称和摘要不改写为正式版本。正式发布使用提交后的固定 `v1.13.1` Tag，已完成 Linux 完整测试、双架构构建、安装包校验和镜像版本检查，实际结果见顶部正式发布记录；服务端核心依赖沿用上表版本。

从本版起，新正式 Tag、Release 和程序版本使用 `v<主版本>.<次版本>.<修订号>`；上游基线单独记录。历史 `-yz.N` Tag、Release 与镜像保持原样，安装器和 `xbctl upgrade` 继续支持显式下载旧版。发布顺序为 Node `v1.13.1`、面板 `v1.13.3`。

以下各节保留此前的开发和历史发布记录，其中“未发布”“待发布”等状态仅对应当时的验证阶段。当前正式版本及最近回滚基线以顶部 `v1.14.0` 发布记录为准。

## 直连出站切换兼容（v1.13-yz.24，未发布）

| 项目 | 标识 |
| --- | --- |
| 目标 Node 版本 | `v1.13-yz.24`；当前为本地源码修改，尚未创建 Tag、Release 或发布镜像 |
| 修改基线 | `b5ce51f5dea000545d8a4ea6adde0074a9ee7146` |
| 修改范围 | Node 的 sing-box 直连出站参数转换、单一源地址约束、错误传递及相关测试 |
| 主控兼容 | 沿用现有 `custom_outbounds` 与节点内核选择字段，面板无需修改保存格式 |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`，未修改 |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427`，未修改 |
| 旧配置兼容 | 转换 `ForceIPv4`／`ForceIPv6`、`AsIs` 和单一源地址绑定；原始面板配置不被改写，其他原生选项按目标内核校验 |
| 验证状态 | 本地全量 Go 测试、相关依赖测试和双架构构建通过；数据竞争检测与安装器路径测试受本机环境限制，详见下文；尚未发布或更新服务器 |

2026-09-09 本地验证使用 Go `1.26.4`、`windows/amd64`，完整功能标签为
`with_quic with_utls with_wireguard with_acme with_clash_api`。

| 验证项 | 结果与范围 |
| --- | --- |
| 原问题复现 | 修改前的 sing-box 配置解析以 `unknown field "domainStrategy"` 失败，与服务器诊断结果一致 |
| 全量 Go 测试 | `go test -mod=readonly -count=1 -p=1 -tags "with_quic with_utls with_wireguard with_acme with_clash_api" ./...` 通过；未启用 `-race` |
| 出站兼容回归 | 固定内核解析、参数冲突、原始配置不变、SS2022 AES-128／AES-256、域名 TCP／UDP、IPv4／IPv6 实际源地址、错误地址族拒绝、路由预解析、UDP 会话后续目标和 IPv4 映射地址通过 |
| 生命周期回归 | 首批用户、重复同步与重载、停止恢复、错误配置修正、旧格式与原生绑定互换及流量恰好累计通过 |
| 依赖回归 | `Makefile` 列出的 AnyTLS、两份 SS2022、Xray singbridge／Hysteria 和 sing-box gRPC 共六个包的普通测试通过；依赖未修改 |
| 安装器测试 | `tests/install_service_manager_test.sh` 通过；`tests/install_paths_test.sh` 在 Git Bash 默认模式下因符号链接被复制为普通文件而无法通过 `fresh` 场景，启用 `MSYS=winsymlinks:nativestrict` 后明确报 `Operation not permitted`；未修改或弱化测试 |
| 数据竞争检测 | 已尝试设置 `CGO_ENABLED=1` 运行 `go test -race`，编译阶段报 `C compiler "gcc" not found`，未完成检测 |
| Linux 构建 | Node 与 xbctl 的 `linux/amd64`、`linux/arm64` 构建通过，使用 `-mod=readonly -trimpath -buildvcs=true`、`CGO_ENABLED=0`，临时版本为 `v1.13-yz.24-dev` |
| 构建来源检查 | 四个程序的 `vcs.revision` 均为上述修改基线，`vcs.modified=true`；Node 的内核版本、替换模块与校验值保持上表固定依赖；这些是本地未提交修改的验证产物 |

跨内核转发测试仅使用回环监听、内存生成的测试身份和本地 DNS。Xray 测试配置单独放行回环目标，以满足其 freedom 出站的默认私有地址限制。
正式发布前仍需在具备 C 编译器和原生符号链接的 Linux 环境完成 `make test`；本次没有执行 Linux 程序或更新生产节点。

## 自动防火墙与端口跳跃（v1.13-yz.24，待发布）

| 项目 | 标识 |
| --- | --- |
| 目标 Node 版本 | `v1.13-yz.24`；本轮为功能开发和隔离验证，尚未创建正式 Tag、Release 或镜像 |
| 功能开发基线 | `1d74d19cbb738e6fcde466361c24a186b8deec60` |
| 功能提交 | `ade17c59c021fdeeec7a19c4ebb5e5cb9ccce17f` |
| 并行安装器改动 | 合并固定提交 `b5ce51f5dea000545d8a4ea6adde0074a9ee7146`，保留 `v1.13-yz.23` 的自定义程序目录及升级回滚 |
| 已验证构建来源 | `7b3a7b434790ecf7238b4977d02db5610b4db7e5`；独立分支构建 `v1.13-yz.24-dev`，Node 与 xbctl 的两个 Linux 架构、来源及摘要均通过检查 |
| 配套面板 | `1.13.0`，功能提交 `cee871e25a54b180e384346689664f538c8c2887`；通过 `port_hopping` 下发端口集合，`server_port` 保持单一监听端口 |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6` |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427` |
| 修改范围 | Node 防火墙生命周期、UFW/firewalld、nftables/iptables、端口集合、配置和 xbctl 配置保留；没有修改核心仓库或依赖 |
| 验证记录 | [防火墙与端口跳跃验证](docs/firewall-validation.md) |
| 使用和回滚 | [配置说明](docs/firewall-port-hopping.md)；回退 Node `v1.13-yz.23` 前先准备手工规则或取消跳跃，再正常停止新版回收规则 |

下方正式发布引用仍指向已经发布的版本。本轮构建来源和校验值单独记录，不将开发构建列为正式发布。

## 自定义程序目录（v1.13-yz.23）

| 项目 | 标识 |
| --- | --- |
| 当前正式 Node 版本 | `v1.13-yz.23`；Tag、Release、双架构安装包及镜像已发布并校验 |
| 正式来源 commit | `1944c8eb7982c4f6156d6adff8e8a734cdc1813b` |
| 本次修改基线 | `1d74d19cbb738e6fcde466361c24a186b8deec60` |
| 修改范围 | 安装器、xbctl 的程序目录与升级回滚、systemd/OpenRC 服务路径及相关测试 |
| 配置与主控兼容 | 保留 `/etc/xboard-node` 和原日志位置；主控通信、配置格式及两个内核的依赖均未变更 |
| 旧安装 | 无目录记录时使用 `/usr/local/bin`；可使用新安装器的 `upgrade --bin-dir` 迁移 |
| 旧版本回退 | 自定义目录下拒绝缺少目录支持的旧 xbctl；先用新安装器迁回默认目录，再回退到 `v1.13-yz.22` 等旧版本 |
| 验证范围 | 本地和 Linux CI 的安装、迁移、错误与中断恢复；正式产物验证见下文，过程与限制见 [自定义安装目录说明](docs/custom-install-directory.md) |

## HY2 ECH 前置入口（v1.13-yz.22）

| 项目 | 标识 |
| --- | --- |
| 该版 Node 版本 | `v1.13-yz.22`；Tag、Release、双架构安装包及镜像已发布 |
| 本次 Node 修改基线 | `0066db507d5fe26698528175d69e30522fa2f4ce` |
| 配套面板版本 | `1.12.0`，正式来源 `c2d6873ec055dbb8d48184eb296d50db6c85e529` |
| Xray 固定依赖 | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6`，本次没有修改核心 |
| sing-box 固定依赖 | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427`，本次没有修改依赖 |
| 修改范围与验证 | HY2 ECH 密钥写入、运行配置与中转校验、实际握手及中转；见 [验证记录](docs/hy2-ech-validation.md) |

以下记录 `v1.13-yz.23` 的正式发布结果；Node 回滚基线为 `v1.13-yz.22`，面板沿用 `1.12.0`。`yz.22` 的验证保留在历史发布记录中。

## v1.13-yz.23 发布引用与回滚基线（历史记录）

| 项目 | 标识 |
| --- | --- |
| Node 本版版本 | `v1.13-yz.23`；固定来源使用同名 Git Tag |
| Node 本版来源 commit | `1944c8eb7982c4f6156d6adff8e8a734cdc1813b` |
| 上一正式 Node 版本（回滚） | `v1.13-yz.22`；自定义目录安装需先用新版安装器迁回 `/usr/local/bin` |
| Node 适用分支 | `upgrade/singbox-v1.14.0` |
| Node 上游发布基线 | `v1.13` |
| Node 上游基线 commit | `0a29338e1f102a462363ce3527417029f89bab28` |
| Node 本版 Release | [v1.13-yz.23](https://github.com/P0me1oo/YZboard-Node/releases/tag/v1.13-yz.23)；完整来源、架构和校验值由附件中的构建信息与 SHA256SUMS 固定 |
| Node 回滚 Tag / commit | `v1.13-yz.22` / `2aa021b65481a14b1d34ff9f594939387c5f0f05` |
| Node `yz.19` 修复基线 | `89c2753390356f51df3d8fc133ae8064fa8ed669` |
| Node `yz.17` 验证构建 commit | `ada7bb60b18bf14b80e171030b82bc0f3412beb6` |
| Node Release 构建工具链 | `Go 1.26.4`（`go.mod` 要求 `go 1.26`） |
| Node Release 构建 | Node、xbctl 的 `linux/amd64` 与 `linux/arm64` 安装包、安装器、四份构建元数据和 SHA256SUMS，共 10 个附件 |
| Node 本版 Docker 标签 | `ghcr.io/p0me1oo/yzboard-node:v1.13-yz.23`；完整提交标签和 `latest` 已同步并核对同一 digest |
| Node 本版 Docker manifest | OCI index `sha256:e041bab08ea982bd205cbe72e93509175de79cbb2c827f73e80d7c7ea76810c2`；包含 `linux/amd64` 与 `linux/arm64` |
| Node 回滚 Docker manifest（yz.22） | OCI index `sha256:8c831eca80ebc66680055e0f6f443a2c0e1830a28fc6bab638fc7a0487a54235`；包含 `linux/amd64` 与 `linux/arm64` |
| 历史 Docker manifest（yz.16 记录） | OCI index `sha256:f3e0895ebc04ac603158a5b96413e7695e5c7a1abd596ee864d7d4852b0b4665`；用于历史版本审计 |
| Node 本版 Docker OCI 标识 | 两架构均为 `revision=1944c8eb7982c4f6156d6adff8e8a734cdc1813b`、`version=v1.13-yz.23` |
| YZboard 兼容版本 | `1.12.0`；沿用 `yz.22` 的通信与内核配置，本次安装目录修改不要求更新面板 |
| YZboard 本版来源 commit | `c2d6873ec055dbb8d48184eb296d50db6c85e529`，Tag `v1.12.0` |
| YZboard 本版镜像 | `ghcr.io/p0me1oo/yzboard:1.12.0-c2d6873`；manifest `sha256:39065c1b1fb66537e62f8b18f8c44c21a8ae0e64b3d120944573a17cfd471aa6` |
| 历史 YZboard 兼容代码 | `f91568d72ffb55205cbcd9b15a8476283a017683`（面板 `v1.11.0`） |
| 历史 YZboard 镜像（1.11.0） | `ghcr.io/p0me1oo/yzboard:1.11.0-f91568d`；manifest `sha256:9ec52732a2f93f77e1ae6f34e314cf9399b26a8c4febf4a82db2e32cd7e651b4` |
| Xray 官方仓库 | `XTLS/Xray-core` |
| Xray 上游预发布 Tag | `v26.7.11` |
| Xray 上游 Tag commit | `50231eaff98ccc31b5cbd247a721c16e97fe5ec1` |
| YZ-Xray-core 源码 Tag | `v26.7.11-yz.6` |
| YZ-Xray-core 当前已固定版本 / commit | `v26.7.11-yz.6` / `b4caa82d6414196565599c19ebc1b53e331349b6` |
| Node 当前 Xray replace | `github.com/P0me1oo/YZ-Xray-core v0.0.0-20260907200713-b4caa82d6414` |
| Xray 模块校验值 | `h1:s4BnktK25n8oj8+sQmfD2n86e+JmekassicYU4F+YFs=` |
| YZboard 兼容标识 | `xray-v26.7.11-yz.6`（面板 `1.12.0`） |
| sing-box `require` 版本 | `v1.14.0` |
| sing-box 实际 replacement | `github.com/P0me1oo/YZ-sing-box v1.14.0-yz.2` |
| sing-box 官方基线 | `v1.14.0` / `0b8995879f29a9b98ee027bc17b75e101445b238` |
| sing-box 兼容仓库 | [P0me1oo/YZ-sing-box](https://github.com/P0me1oo/YZ-sing-box)；保留用户、路由热更新和 Mieru |
| sing-box 兼容 Tag / commit | `v1.14.0-yz.2` / `09615a105e219076330d9d2a25ea1e2e733d5427` |
| sing-box 模块校验值 | `h1:KL0agFYXpL1qEUsa+toI8VhlUVuQser2AVYL3unVkKk=` |
| sing-box 验证依赖 | 正式 `go.mod` 使用远程固定 Tag；前期隔离实测使用同一兼容源码的本地 replacement |
| AnyTLS 上游基线 | `anytls/sing-anytls v0.0.11` / `130d2e61b8895727bfed4942c535e91b246a9603` |
| AnyTLS 实际 replacement | `./compat/sing-anytls`，随 Node 固定提交构建；仅修复流关闭状态和回调的并发访问，来源与移除条件见 [补丁说明](compat/sing-anytls/README.yz.md) |
| SS2022 原模块基线 | `sing-shadowsocks v0.2.8` / `e0612494bafdd1429e9632bc52fd278585d28690` |
| SS2022 实际 replacement | `./compat/sing-shadowsocks`，关闭补丁随 Node 固定提交构建，来源与移除条件见 [补丁说明](compat/sing-shadowsocks/README.yz.md) |

Node 自身版本保持独立，不伪装成 Xray 版本。Node 延续上游 `v1.13` 版本线；`yz.5` 支持首版 Shadowsocks 中转，`yz.6` 至 `yz.9` 延续既有安装、出站和用户同步修订，`yz.10` 新增 VLESS 落地、VLESS Encryption 和当前 Xray 传输矩阵，`yz.11` 为安装器与 `xbctl` 增加 Alpine Linux/OpenRC 生命周期支持，`yz.12` 修复机器模式首个用户同步与失败回滚，`yz.13` 增加 REST/WS 双通道对账、ETag 事务回滚和权威设备快照，`yz.14` 增加 SS2022 进程内时间校准、健康状态和主动诊断，`yz.15` 增加机器模式节点级内核选择，`yz.16` 增加用户-落地节点流量归属上报，`yz.19` 明确失败配置停止和健康失败状态，不自动恢复旧配置。Xray 的上游版本、YZ fork patch 版本和 Node 发布版本分别记录，便于升级、回滚和定位构建来源。

先前的 `v0.1.0-yz.1` Tag 保留用于审计，但其版本低于上游 `v1.13`，不作为部署或升级目标，也不创建对应 Release。

`yz.17` 将 sing-box 官方基线更新为 `v1.14.0`，修复 UDP 统计、用户更新并发、稳定认证身份、路由更新回滚和 Mieru 监听生命周期。兼容源码与 Node 的 Xray 依赖独立；本次没有修改 YZboard 或 YZ-Xray-core。

正式 `go.mod` 与 `go.sum` 固定两个主内核的远程版本和校验值；AnyTLS 与 SS2022 兼容源码随 Node 提交固定。服务器安装版本由用户执行升级后改变。

## v1.13-yz.23 发布验证（2026-09-09）

Node 的 [发布前 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34269113262/attempts/2) 与
[正式发布 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34270886003) 均从
`1944c8eb7982c4f6156d6adff8e8a734cdc1813b` 执行完整 Linux `make test`。
各完成 690 项 Go 测试及子测试、20 个新增安装器场景和已有服务文件测试，无失败、无跳过、无数据竞争报告。
发布前发现并修复了 Linux Bash 信号退出后重复回滚的问题；另一次既有 HTTPUpgrade 用例的 `EOF`、同提交重跑和本地复验结果见 [过程记录](docs/custom-install-directory.md)。

正式 Release 发布时间为 `2026-09-08T19:57:07Z`，对应新加坡时间 2026-09-09。
10 个附件已下载，逐个核对 GitHub 附件摘要与 `SHA256SUMS`；安装器内容与固定提交一致。
四个二进制的实际构建信息与附件逐项一致，均为 Go 1.26.4、对应 Linux 架构、`CGO_ENABLED=0`、
上表来源和 `vcs.modified=false`。Node 程序实际解析到上表固定内核、模块校验值和本地兼容模块；xbctl 不链接内核。
CI 已运行 amd64 的 Node、xbctl，以及两个架构的 Node 镜像，确认版本与来源；arm64 镜像版本检查通过 QEMU 执行。

| 正式产物 | SHA256 |
| --- | --- |
| `xboard-node-linux-amd64` | `849ac862547850481d4d49c1697858088c4d31be2924a33d611e5bf1b4197992` |
| `xboard-node-linux-arm64` | `803cc7886843d60efb48dbc3ecaec7b27aa2bb63dde90b49ca0fa4e2ee1101a6` |
| `xbctl-linux-amd64` | `6f11c35e2bc4141c512640f3eed24f12999fe4a962c686d52eedaa132f675190` |
| `xbctl-linux-arm64` | `c6d00ae4f429718b08656829b34b1e3f001c9d6da51b2e57cd54b29a8735ffee` |
| `install.sh` | `12ff414f6004a5a4b886055904c39cc75ee7c772cc3a6717546c4079824a7ef1` |
| `SHA256SUMS` | `dd4f56e44742445ff5a564d1170b5b3cf9df1dbe4dc62457752eae949b6fdeb9` |
| `xboard-node-linux-amd64.buildinfo.txt` | `3e04725b37960b68eb4a737b7545200cc712fc4f96e237dcab25bfa75dd26d05` |
| `xboard-node-linux-arm64.buildinfo.txt` | `eebc37582244cd595736fd7e1e62970635d92b0929644191d9e7540b2f409e71` |
| `xbctl-linux-amd64.buildinfo.txt` | `3191f6954cee9a529680efb6bafcdd4cc50f7c7105defe21dad08a76394dca68` |
| `xbctl-linux-arm64.buildinfo.txt` | `ddfaf1f40728080b68581bafb392df45e3c4c95b0e750e832a4bf6a8c838ca60` |

版本标签、完整提交标签与 `latest` 均指向上表 OCI index，已通过匿名读取确认；两个架构的 OCI revision 与 version 一致。

| 镜像平台 | 平台 manifest |
| --- | --- |
| `linux/amd64` | `sha256:1e363a6a2e0d6e83eb827bf2dd076625acc50be7669437c9a8c3541c902e2ad2` |
| `linux/arm64` | `sha256:22168dbb80120bb5919c23399624e027ff91bcdf071eca71c77bdeb72014ff2a` |

安装器测试使用隔离目录和模拟服务；本次未进行生产迁移、Linux 实机服务与系统重启挂载验收或 arm64 实际转发测试。
回退至 `v1.13-yz.22` 前须使用新版安装器将自定义目录迁回默认目录，并保证默认分区具备所需空间。
发布结果通过单独的文档提交补充，已发布 Tag 与产物来源保持固定。

## 历史发布验证：v1.13-yz.22（2026-09-08）

Node 的 [正式发布 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34172378951) 从 `2aa021b65481a14b1d34ff9f594939387c5f0f05` 执行
Linux `make test`，主模块和六组兼容模块共 661 项测试及子测试通过，无失败、无跳过、无数据竞争报告。
HY2 ECH 测试及子测试为 21 项，包含正常握手、错误公钥拒绝和八组中转组合；外部 Mihomo 客户端的八组联测在 Windows 本地完成。

两个架构的安装包与镜像构建、来源检查和镜像版本运行均通过。10 个 Release 附件已下载核对
GitHub 附件摘要、SHA256SUMS 和实际模块信息，四个二进制均为 Go 1.26.4、对应 Linux 架构、
`CGO_ENABLED=0`、上述 `yz.22` 来源和 `vcs.modified=false`，运行版本为 `v1.13-yz.22`。
两个 Node 程序实际使用上表固定内核与兼容模块；xbctl 不链接内核。

| 正式产物 | SHA256 |
| --- | --- |
| `xboard-node-linux-amd64` | `172eb498e07d421021f9fdc026a4556ff46bb3f86e4722740703ecc53ff2f51a` |
| `xboard-node-linux-arm64` | `151a6da01a1b3faea98d9202a8a8787deccb938fbe3a63834bd8626044298f83` |
| `xbctl-linux-amd64` | `74e273bae6890f07f0ebff28bc812b3b1b708bbf8c0fd1e708b9f12868e95b81` |
| `xbctl-linux-arm64` | `dfabeced808e2dbd954849635235c06803f05b3ffa164ba754d2947f6ff82813` |
| `install.sh` | `19d5556a52da021209f10ad88d06d26e7050747df2ba00a778f08e66a1c2372a` |
| `SHA256SUMS` | `276e4870dea001170f6245317096000d9b278e0c57b7eee2634563b14bc51f39` |

该版发布时，版本标签、完整提交标签与 `latest` 指向 `sha256:8c831eca80ebc66680055e0f6f443a2c0e1830a28fc6bab638fc7a0487a54235`，两个架构的 OCI 来源和版本均一致，支持匿名拉取。
Linux amd64 runner 已执行实际回环转发；arm64 通过 QEMU 运行镜像版本检查，未进行 arm64 实际转发。
本次没有进行真实服务器测试或外部 DNS ECH 配置部署验证。

配套面板 [v1.12.0](https://github.com/P0me1oo/YZboard/releases/tag/v1.12.0) 已发布，面板测试为 60 项、813 个断言；
镜像固定来源、两个架构和 `latest` 已核对。先升级相关 Node，再更新面板和订阅。
回退到 Node `v1.13-yz.21` 或面板 `1.11.0` 前，应先关闭 HY2 入口的 ECH 并刷新订阅。
发布结果以单独文档提交补充，已发布 Tag 与产物来源保持不变。

## 历史发布验证：v1.13-yz.21（2026-09-08）

Node 的 [发布前 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34158600477) 与
[正式发布 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34159504349/attempts/2) 均从 `2f08f4134d352e127828e1e15aeaa4cfd479864c` 执行完整 `make test`，
各完成 637 项测试及子测试，无跳过、无竞争报告。两个架构的安装包与镜像构建、构建来源检查和镜像版本运行均通过。
正式 CI 第一次因回环临时端口被占用而失败；新 runner 使用同一源码、Tag 和全部检查重跑后通过，首次记录保留在验证文档中。

10 个 Release 附件已下载，逐个核对 `SHA256SUMS` 与 GitHub 附件摘要。四个二进制的实际构建信息与附件一致，
均为 Go 1.26.4、对应 Linux 架构、`CGO_ENABLED=0`、上述 `yz.21` 来源和 `vcs.modified=false`。
Node 二进制实际使用上表两个远程内核及随源码固定的 AnyTLS、SS2022 兼容模块；xbctl 不链接内核。

| 正式产物 | SHA256 |
| --- | --- |
| `xboard-node-linux-amd64` | `5ef02f9b680c847758cf6c820f1780f089013d704602b1ee198733ee8f5d6db0` |
| `xboard-node-linux-arm64` | `2b2d604a509446ac17d6cdd58c27807c864cdcf0e19b976d0e4dd602b4cb960d` |
| `xbctl-linux-amd64` | `acc594ec027646aef34d70437572cacf2e8f86935630c5199a918cac3e50daf9` |
| `xbctl-linux-arm64` | `21a141e023c071b689295ddd3d39082eee658f61004b6cd591053a03af14032e` |
| `install.sh` | `19d5556a52da021209f10ad88d06d26e7050747df2ba00a778f08e66a1c2372a` |

完整附件校验清单见 [SHA256SUMS](https://github.com/P0me1oo/YZboard-Node/releases/download/v1.13-yz.21/SHA256SUMS)。
该版发布时，Node 三个镜像标签均解析到 `sha256:500bd8ac445a38ae76550bc2d66c7fd9700515255ab92e9276020bc6136984a0`；面板的不可变标签、`1.11.0` 与 `latest` 也已核对一致，
面板来源构建见 [run 34161072639](https://github.com/P0me1oo/YZboard/actions/runs/34161072639)。两仓库的两个 Linux 架构及 OCI 来源、版本均已逐项核对。

Xray `v26.7.11-yz.6` 的 [三平台测试](https://github.com/P0me1oo/YZ-Xray-core/actions/runs/34158811734)、
[多平台构建](https://github.com/P0me1oo/YZ-Xray-core/actions/runs/34158811725) 和
[Windows 7 打包](https://github.com/P0me1oo/YZ-Xray-core/actions/runs/34158811778) 均通过。
核心依赖以源码 Tag 固定并内嵌在 Node 安装包、镜像中；本轮未另行创建独立核心二进制 Release。

发布后的补充记录使用单独的文档提交，已发布 Tag 与产物来源保持不变。生产服务器的安装版本由用户执行更新后改变。

## `yz.21` sing-box 中转与并发修复

本版配套面板为 `1.11.0`。sing-box 支持
VLESS/HY2 入口和 Shadowsocks/VLESS 落地，可与 Xray 混用。
2026-09-08 固定 Xray `v26.7.11-yz.6` 和 sing-box `v1.14.0-yz.2`，更新 `go.mod`、`go.sum` 和构建标识。
用户身份按「用户 × 线路」展开，以原生 `auth_user` 规则选路，再映射回真实用户进行限速、设备限制和计费。

认证、用户映射和路由更新按过渡规则协调；删除或轮换用户后，旧 HY2 会话的新请求不能退回默认出站。
未知线路不通过认证。用户和线路计数在内核重建、停止恢复时继续累计，落地不加载面板用户。
sing-box 的线路总量按实际出站上的用户有效载荷计算，Xray 保持原有内部出站口径。

任一端使用 sing-box 时，VLESS 内部链路支持 RAW/TCP、WebSocket、gRPC、HTTPUpgrade，
不支持 VLESS Encryption、TCP 头部伪装及无效 Vision 组合。使用步骤见 [中转说明](docs-relay.md)，
本轮运行、两机测试、性能测量和构建记录见 [sing-box 中转验证](docs/singbox-relay-validation.md)。

Xray `yz.6` 保留 VLESS 首批缓冲上传计数修复，并同步 UDP 缓存及 HY2 会话的关闭状态。sing-box `yz.2` 同步 gRPC 初始化与关闭，修复 SS2022 多用户兼容副本的关闭逻辑；原 SS2022 模块的同一修复随 Node 源码固定。用户套餐和用户-线路明细继续使用原有计数路径。

原三类修复在 Xray `yz.5` 阶段分别由新增用例复现，修复后四个相关包各连续 10 轮 `-race` 通过，共 180 项测试及子测试执行。该阶段 Windows/amd64 的 Node 完整普通测试通过：17 个有测试的包、609 项测试及子测试，无失败或测试跳过；面板 57 项测试、549 个断言通过。

Linux/amd64 完整 sing-box 包并发检测耗时 126.042 秒，142 项测试及子测试全部通过，无跳过、无竞争报告；此前失败的混合内核与 gRPC 分支全部保留。YT-HK、DGN-HK 使用修复后依赖完成 60 次转发及生命周期请求，覆盖 TCP/UDP、用户增删、重复同步、重载和停止恢复。原 Xray `yz.4` 下的失败结果保留在验证文档的历史部分。

上述为发布前工作区的本地及双机验证。正式安装包和镜像已从上表固定 Node 提交构建、发布并核验；结果见本版发布验证。升级时先更新相关 Node，再更新面板并启用新拓扑；上一正式版本用于回滚。

发布前完整 Node 并发检测 [run 34155848942](https://github.com/P0me1oo/YZboard-Node/actions/runs/34155848942) 曾在 HY2 会话关闭处发现另一类竞争，该次未进入构建和发布。补充修复固定为 Xray `yz.6`，5 项会话回归连续 10 轮 Linux 并发检测共 50 次通过，无竞争报告；新依赖随后通过了上述完整 Node 发布验收。

## `yz.20` HY2 前置入口基线（并入 yz.21 发布）

本次修改起点为 Node `94e2a76e42c1f059126b588b2d02f49e54fd8246`，配套面板起点为
`eff2fa22531f2e15168d3e7e96d8ab45639b1969`。源码目标为 Node `v1.13-yz.20` 与面板 `1.10.0`，
该轮没有单独创建 `yz.20` Tag、Release 或镜像，相关功能最终随上文 `yz.21` 一同发布。

HY2 前置入口复用 YZ-Xray-core `601226e180d3684a5eabb8bc901c99f499398db1` 的认证 UUID 路由能力，
入口和落地均使用 Xray，内部协议仍为 Shadowsocks/VLESS。该轮固定依赖未变，联调发现 VLESS 出站上传漏计；后续修复与依赖接入见上文 `yz.21` 记录。
同时修正用户热更新成功后未更新配置指纹的问题，避免随后相同配置同步误触发重建、中断 HY2 会话。

`relay` 配置和三类流量报告的结构不变。应先升级 Node，再启用面板中的 HY2 前置入口。
使用方式、证书和混淆要求见 [中转说明](docs-relay.md)。

### 本地验证状态（2026-09-07，Xray yz.3 历史结果）

当时固定核心下，`TestHysteria2RelayRuntime` 的实际 TCP/UDP 路由、Salamander、用户变更、重载、恢复及用户流量断言通过，
但最终 VLESS 落地出站上传计数为零，运行测试因此失败。
根因是 Xray `BufferToBytesWriter` 的普通字节写入绕过计数器，导致 VLESS 首批缓冲写入漏计；用户套餐和用户-落地明细的计数路径不受此问题影响。

用本地临时覆盖补上字节写入计数后，Node 完整 Go 测试及核心 `common/buf` 测试通过。
独立的核心回归用例还覆盖缓冲刷新、部分写入及失败时的实际字节计数，确认多缓冲区写入不会重复累计。
临时覆盖未修改核心仓库和正式依赖，不能作为正式发布验收；需在修复纳入、固定到远程不可变提交后重新验证。

安装器服务文件测试和 AnyTLS 兼容模块测试通过。Windows 环境下 `go test -race` 因未启用 CGO 无法执行；完整 race 检查仍需具备 C 编译器的环境。

使用正式 `go.mod`、关闭临时工作区及覆盖后，`xboard-node` 和 `xbctl` 的 `linux/amd64`、`linux/arm64` 交叉编译通过。
产物已核对目标架构、`CGO_ENABLED=0`、Xray/sing-box 实际 replacement 和 VCS 信息：
`vcs.revision=94e2a76e42c1f059126b588b2d02f49e54fd8246`、`vcs.modified=true`。
这些产物来自未提交工作区，仅用于编译验证，包含的 Xray 仍是上述未修复统计问题的固定依赖；未执行 Linux 运行验收，也未作为 Release 发布。

## `yz.19` 正式发布（2026-09-07）

发布提交为 `d22037477a7e97825990eb35e41d12926c117680`。[发布前 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34066413890) 与 [正式发布 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34066869570) 均通过，两个架构的镜像版本命令与来源提交一致；arm64 镜像运行检查使用 QEMU。

[GitHub Release](https://github.com/P0me1oo/YZboard-Node/releases/tag/v1.13-yz.19) 包含 10 个附件。安装包的 Go 1.26.4、Linux 架构、`CGO_ENABLED=0`、五个 Node 功能标签、实际模块和 `vcs.modified=false` 已核对；发布附件摘要与 SHA256SUMS 一致。完整构建信息和校验值以 Release 附件为准。

| 安装产物 | SHA256 |
| --- | --- |
| `xboard-node-linux-amd64` | `4b6334e15854da2b9ddbc5557a52bc43bc12e986b56c6a3d50516dcb760dd078` |
| `xboard-node-linux-arm64` | `f755ee4c76dd30fd57da40a0f3753373e4eef609d682387ab7327fb64c2f06f7` |
| `xbctl-linux-amd64` | `b3a4e0805ffbb6edd4262b576129083d39971a3707fc4c911839c797e71db1b6` |
| `xbctl-linux-arm64` | `9ab77eff9cc7f6c2bfb9139c07599d9211038a35a3d85aeed392722f3609193c` |
| `install.sh` | `19d5556a52da021209f10ad88d06d26e7050747df2ba00a778f08e66a1c2372a` |

固定镜像引用为 `ghcr.io/p0me1oo/yzboard-node@sha256:8b65c52c0c0f59a24c56ab48ced7a1dda9a07c6948edc3f454b41c140d999818`。`v1.13-yz.19` 与 `latest` 均指向该 index；amd64 manifest 为 `sha256:826ac2d52b00bef1510080bb79f9b76dc12bd8844ad3856f38cf3a5fa5c0cb54`，arm64 manifest 为 `sha256:d93cee9e308cfa77c448031f992ade9e45042647b0edc313ee6041dffc09f064`。两份镜像配置中的 OCI revision 和 version 均与本次发布一致。

上一正式 Node 版本为 `v1.13-yz.16`。本次未执行生产服务器升级；升级时同步服务停止等待时间为 150 秒，具体要求见下文。

## `yz.19` 失败状态修复（2026-09-07）

配置、协议、端口、出站或用户应用失败时，Node 直接记录包含操作、内核和底层原因的错误，停止当前内核并将节点健康状态标记为失败。面板最新的失败配置和用户快照会保留为待修正状态；相同失败快照不会被定时采样、REST 或 WebSocket 反复启动，只有配置或用户实际变化后才允许重新尝试。首次启动失败的进程继续保持控制通道和健康端点，等待修正后的面板配置。

本次只修改 YZboard-Node，未修改 YZboard 或 YZ-Xray-core。详细行为和测试命令见 [Node 修复验证](docs/node-reliability-validation.md)。

配置校验在节点服务内统一执行，REST、WebSocket 和首次同步均保留无效快照并停止对应内核；用户增删不能绕过配置或证书错误。删除问题用户会尝试启动剩余用户，单个节点或实例初始化失败不会取消同一进程的其他节点。整体健康端点返回 503 表示存在失败项，其他节点仍可继续转发。

首次发布前 CI 在 AnyTLS 用户删除测试中捕获上游 `dieErr`、`dieHook` 数据竞争，因此未发布该构建。Node 内增加固定上游源码的最小兼容补丁，保留完整生命周期测试，并单独执行依赖包的并发测试。两个主内核的固定版本未变；AnyTLS 本地 replacement 的身份由 Node 提交及原始文件校验清单共同记录。

`yz.19` 最终开发验证已完成：Windows amd64 通过 18 个包、586 项测试及子测试；YT-HK Linux amd64 `-race` 通过 5 个包、331 项测试及子测试，失败、跳过和数据竞争均为 0。AnyTLS 底层关闭回归重复 50 轮、真实用户生命周期重复 20 轮全部通过。12 个实际进程场景通过，包括两种多节点模式下的端口冲突、无效出站与初始 HTTP 失败隔离，以及等待 18 秒的退出报告和失败重试。Node、xbctl 双架构构建和 amd64 运行时版本检查通过；arm64 未进行实机运行。验收包为 `runtime-validation.tar.xz`，大小 130250888 字节，SHA256 `aa3f59474867e7c727beb793af1186044b44ab97dee2298da40f49bcbd67f424`。原有 7 个监听未变化，临时目录、测试进程和上传包已清理，Netcatty 会话已关闭。

进程退出等待两分钟，安装器的 systemd/OpenRC 模板等待 150 秒。已有部署通过 xbctl 单独替换二进制时还需同步服务停止等待设置；Docker/Compose 也应设为 150 秒。发布 CI 核对完整来源提交、干净源码标识与双架构元数据，Docker 版本检查通过后才更新正式标签。

## `yz.18` 可靠性修复（2026-09-06）

该轮只修改 Node，沿用当时固定的 sing-box、Xray 依赖和既有面板接口。修复范围包括内核监听退役、出站应用与失败恢复、空用户同步、REST 用户重试、跨实例流量累计和退出上报。详细不变量、执行命令和验证状态见 [修复验证](docs/node-reliability-validation.md)。

`yz.18` 开发构建来自当时未提交的修复，保留实际的 `vcs.modified=true` 标识。该轮正式发布前需要提交、固定 Tag、重新构建并更新记录；下文 `yz.17` 的历史校验值不能用于 `yz.18` 修复产物。

Windows amd64 全量测试通过：17 个包、555 项测试及子测试；`go vet` 与安装器脚本检查通过。YT-HK 上实际执行四个 Linux amd64 `-race` 测试包，共 301 项测试及子测试，全部通过、无跳过、无数据竞争。Node、xbctl 双架构构建和 amd64 运行时版本检查通过；arm64 尚未进行实机运行。测试产物和结果按 [修复验证](docs/node-reliability-validation.md) 归档，本次远程测试文件已清理。

验证归档为 `yznode-v1.13-yz.18-test-linux-89c2753.tar.gz`，大小为 206497020 字节，SHA256 为 `7ced3892a85b142ff873efea731149123f3c61afb2287f0794b1c69d06a719a3`。归档包含八个二进制及其校验值、源码清单、构建元数据和本次 Linux 实测结果；逐文件校验值以包内 `SHA256SUMS` 为准。

## `yz.17` 固定依赖验证（2026-09-05）

远程 Tag 指向上表中的完整兼容提交。下载的 Go 模块中，1187 份源码、模块文件及第三方来源文件与该提交的原始 Git 内容逐字节一致；657 项模块版本与隔离实测使用的依赖列表一致。`go mod verify` 通过，Xray replacement 保持原有固定提交。

使用正式 `go.mod` 在 Windows amd64 执行 `go test -mod=readonly -count=1 -tags 'with_quic with_utls with_wireguard with_acme with_clash_api' ./...`，全部 17 个测试包通过，共 522 项测试及子测试，没有失败或跳过。本轮为普通测试；相同源码和依赖版本的 Linux race 结果见下文。

以下产物从 `ada7bb60b18bf14b80e171030b82bc0f3412beb6` 的干净源码构建，版本为 `v1.13-yz.17`，构建时间为 `2026-09-05T14:14:13Z`，工具链为 `Go 1.26.4`，`CGO_ENABLED=0`。`go version -m` 已确认两个 Linux 目标架构、远程固定的 sing-box 与 Xray 模块，以及一致的源码提交和 `vcs.modified=false`。完整构建参数和校验值由产物附带的 `build-metadata.json` 与 `SHA256SUMS` 保存。

| 固定依赖构建产物 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `9e71a840ab5716eb005c7ad4d8ff7fbb8f5c42a335dd709cb32e6f618d656ddf` |
| `xboard-node-linux-arm64` | `0f7884d02c2902a7df1198da3ffc8882af444b62e212f83a41ac5fe985a011dc` |
| `xbctl-linux-amd64` | `c7ba1724e0852168bb0795d09fe7944d6bc6c9daa4b349dddaba589040d961cb` |
| `xbctl-linux-arm64` | `5b0086dde3af4a69f960e77962d1af7a8f40040921e9177be4888b573fb13bba` |
| `install.sh` | `8e7c5c21210020f283ebc793e7c6deb8b389648060dc689b88653293c6c6d8dc` |

## `yz.17` 开发验证（2026-09-05）

该轮结果对应 `v1.13-yz.17-test`。Node 使用升级分支的未提交改动及当时的本地兼容核心；这些开发结果不代表正式发布。测试方法和未覆盖的场景见 [sing-box 升级验证](docs/singbox-v1.14-validation.md)。

| 检查 | 结果 |
| --- | --- |
| 核心普通测试 | `./route ./route/rule` 通过；包含规则集事务、并发匹配、初始网络通知和连接转交 |
| Linux amd64 race | Node 全部 17 个测试包及核心 `route` 包通过；未跳过测试，数据竞争报告为 0 |
| 协议与用户生命周期 | 13 个 TCP 场景、10 个 UDP 场景通过；覆盖重复增删、已有连接、新流、重载及恢复 |
| 空用户重启 | SOCKS、HTTP、普通 Shadowsocks、SS2022 AES-128 均拒绝未授权连接，重新添加用户后恢复 |
| Mieru 监听生命周期 | TCP/UDP 停止后重启通过，端口被占用时明确返回启动错误 |
| 安装器检查 | 服务管理器测试、Bash 语法和 Python 语法检查通过 |
| YT-HK 隔离安装 | 使用最终 amd64 产物完成安装、重复安装、模拟面板用户同步、路由回滚及恢复、流量与状态上报、systemd 重启 |
| 原服务与清理 | 原服务 PID 和程序校验值未变化；一次性实例、凭据、上传程序和测试日志均已清理，测试 SSH 会话已关闭 |
| 目标架构 | `linux/amd64`、`linux/arm64` 的 Node 和 xbctl 构建通过；arm64 仅完成构建与元数据检查 |

构建时间为 `2026-09-05T13:13:22Z`，工具链为 `Go 1.26.4`，可安装产物使用 `CGO_ENABLED=0`。`go version -m` 已确认两个目标架构、sing-box `v1.14.0` 的本地 replacement、固定的 YZ-Xray-core replacement，以及 Node 的 `vcs.revision=7802e87136e62ebfc79048207b39323556c7cabc`、`vcs.modified=true`。这些是开发构建的实际标识；正式依赖固定后必须重新提交并构建发布产物。

| 开发产物 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `8734c3e69d773ad8167545a5566d1eb9126fe7bb93b0a3cf142c17f1a44e9ae6` |
| `xboard-node-linux-arm64` | `191840244cbb1d4509e9ae5e12642349e029e3f7957f45716fa8974fb3424242` |
| `xbctl-linux-amd64` | `a46b5e529b319be5b85a92c78b77a8bb078dadb57574d0799b48b78a1e183272` |
| `xbctl-linux-arm64` | `b8442e4a1d267c381353718e3049a761289cadd1300a94e6b10b74b8cf9bba3f` |

真实安装使用的开发包 SHA-256 为 `369862973dcc38a9551fc8d04f161bc4084a93ea0da76d2171f94d6df0fbf245`；包含全部 18 个测试二进制的 race 包 SHA-256 为 `eedf0cda1bf48f5b8b2c91985f0482c1f06322e79ab5875e024465bea4fd3ca0`。上传后已在服务器重新校验。

上述校验值对应使用本地 replacement 的开发产物。使用远程固定依赖重新构建时，应以该次产物附带的 `build-metadata.json` 和 `SHA256SUMS` 为准，不沿用开发产物的校验值。

## 兼容约束

- Xray 的 Hysteria2 用户转换使用 v26.7.11 的 `hysteria/account.MemoryAccount{Auth: ...}`，同时保留 `MemoryUser.Email` 的 `user@<id>` 映射。
- Xray fork 提供的 Dispatcher、用户级限速、统计计数器和在线 IP/连接状态能力继续由 Node 使用。
- Node 的流量方向保持 `[upload, download]`，由内核累计计数器交给 tracker 计算增量，再由面板客户端上报。
- 中转入口的 `relay_user_traffic` 形状为 `user_id => logical_node_id => [upload, download]`，只用于用户-落地归属分析，不参与套餐扣除；`relay_traffic` 继续负责落地节点总量。
- 每次刷出的报告批次带有进程启动标识和递增序号组成的 `report_id`；HTTP 失败时保留完整批次并复用 ID，避免面板重复累计。
- Xray REALITY 入站的 `realitySettings.minClientVer` 由 Node 显式写入，默认 `0.0.0`，可通过 `kernel.reality_min_client_ver` 覆盖。缺省该字段时 v26.7.11 会使用内置下限 `26.3.27`，低于该版本的客户端握手会被拒绝。
- v26.7.11 已移除未加密 Shadowsocks。历史配置中的 `none`/`plain` 会显式返回错误，不会静默转换成其他加密算法。
- `go.mod` 的 Xray `require` 版本只用于保持模块路径兼容；实际代码由 `replace` 固定到上表中的 fork pseudo-version。提交前应使用 `go list -m -json github.com/xtls/xray-core` 复核替换路径和版本。
- Xray 中转通过携带线路编号的认证身份和 `vlessRoute` 规则选路；sing-box 中转按用户与线路生成认证身份，并使用原生 `auth_user` 规则。Node `yz.21` 起入口与落地可混用两种内核，内部传输按两端共同能力校验。
- 面板 `relay` 段与 `relay_traffic` 上报字段属于 YZboard `1.1.0` 起的接口；旧面板不下发该字段时 Node 行为不变。
- 安装器从 `yz.6` 起默认写入 `kernel.type: xray`；`yz.15` 起代码层缺省也按 Xray 处理空值。机器模式下节点的面板 `kernel_type` 优先于机器级默认值；独立实例仍可显式执行 `xbctl config kernel <xray|singbox>` 切换。
- xray 可承载的入站协议为 vmess、vless、trojan、shadowsocks、hysteria；tuic、naive、anytls、mieru、socks、http 只能由 sing-box 承载。安装器和 `xbctl config kernel` 都会在未显式确认时拒绝把这些节点切到 xray。
- 自定义出站从 `yz.7` 起接受 `direct`/`freedom` 与 `block`/`blackhole`，由 Node 翻译成目标内核的原生名；`settings.send_through` 在 xray 下提升为 outbound 级的 `sendThrough`。`settings` 内其余字段原样透传，需按目标内核的字段名填写，跨内核切换时要同步调整。
- 从 `yz.8` 起，同一用户 ID 的 UUID 变化会被视为凭据替换，Xray `UserManager` 必须先删除旧凭据再添加新凭据；任一步失败都不得推进 Node 内部用户状态，并由 Service 尝试使用完整用户集重建内核。
- 从 `yz.9` 起，Xray 的 Shadowsocks 2022 动态用户密钥按面板约定从 UUID 前 16 或 32 字节生成标准 Base64；静态启动配置和运行时增删用户必须得到同一密钥。
- 从 `yz.10` 起，中转 child/landing 同时接受 Shadowsocks 和 VLESS。VLESS 的入口客户端参数放在 `relay.children[].vless`，落地内部身份放在 `relay.vless`；服务端顶层继续承载 `decryption`、Reality 私钥和证书配置。
- 两端均为 Xray 时，VLESS relay 支持 RAW/TCP、WS、gRPC、XHTTP、HTTPUpgrade、mKCP、Hysteria；Reality 只允许 RAW/TCP、gRPC、XHTTP，Hysteria 必须使用 TLS，H2/HTTP 和 mKCP header/seed 会在启动前拒绝。任一端使用 sing-box 时，内部传输限于 RAW/TCP、WS、gRPC、HTTPUpgrade，并按传输校验安全组合。
- `yz.10` 当时沿用已固定的 YZ-Xray-core pseudo-version，本项协议扩展未新增核心补丁。入口和落地 JSON 由该核心自带解析器覆盖验证。
- `yz.11` 的安装器自动识别正在运行的 systemd 或 OpenRC。OpenRC 路径固定使用 `/etc/init.d/xboard-node`、`supervise-daemon` 和 `default` runlevel，日志写入 `/var/log/xboard-node.log`；凭据仍保存在权限为 `0600` 的 `/etc/xboard-node/credentials.env`，启动脚本只按 `KEY=VALUE` 解析，不执行其中内容。
- `yz.14` 的 SS2022 时间校准只在 Node 进程内提供可选时间函数，不修改系统时间。Xray 需要 `v26.7.11-yz.2` 的上下文时间服务补丁；sing-box 使用相同服务，避免每个实例重复查询 NTP。
- `yz.15` 的机器模式按节点创建独立内核服务；只有发现到 sing-box 节点时才创建 sing-box 服务。节点内核变化只重启目标节点，Xray-only 机器不会启动空的 sing-box。

## `yz.14` 时间校准兼容约束

- 普通 SS2022 入站、VLESS 前置中的 SS2022 出站和落地 SS2022 入站共享同一校准结果。VLESS 客户端入口本身不依赖该时间戳。
- 校准器默认并行查询三个 NTP 源并使用有效偏移中位数；查询失败不会猜测时间，最近成功结果超过三个查询周期后回退系统时间。
- `/healthz` 的时钟降级保持 HTTP 200；只有节点组件启动中或失败继续返回 HTTP 503。`xbctl doctor time` 的异常状态返回非零退出码。
- 当前 `go.mod` 已固定 YZ-Xray-core `v0.0.0-20260907151131-4c8f533bce32`，对应 fork `v26.7.11-yz.4` 和 commit `4c8f533bce3258e1c03469d5a6013152988aac04`。正式构建前仍需确认该核心提交可回滚，并不得改回本地路径 replace 或移动分支。

## `yz.13` 同步兼容约束

- WebSocket 仍用于即时推送，但 Node 在连接正常时至少每 5 分钟执行一次 REST ETag 对账；面板推送丢失不会再让配置或用户状态长期停留在旧版本。
- 一次 REST 对账只有在配置、用户和配置规范化全部成功后才提交 ETag。内核应用失败时 Node 保留失败快照和失败状态，不会用旧配置继续运行；相同快照不自动重试，面板下发新配置或新用户状态后才重新应用。
- Node 的 `alive` 和 `online` 都是权威全量快照。空对象表示没有在线设备或用户；YZboard `1.7.0` 会据此清理旧缓存并把在线人数写为 0。
- 设备快照同时通过周期 HTTP 报告和 WebSocket 上报，两条路径读取同一份不可变快照，不再互相消耗。sing-box 的跨节点设备状态按 2 分钟判断过期。
- 流量报告继续复用 `report_id`。YZboard `1.7.0` 会先持久化报告并在单个数据库事务中结算；升级面板时必须执行新增迁移，否则 Node 会持续保留并重试未被接受的批次。

## 构建与版本检查

发布构建示例：

```bash
VERSION=v1.13-yz.11 make build-linux
```

两个二进制的 `-v`/`version` 输出都包含：

- Node 自身版本、构建时间和提交短 SHA；
- Xray 上游 Tag/commit、YZ fork 版本/commit，以及实际模块替换版本；
- sing-box 请求版本和实际 replacement 版本。

`v1.13-yz.15` Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `9f10f11ed8d449b63893cdd7cdb150c7d3fa37239a12968b5e6f71c1c7d7e41d` |
| `xboard-node-linux-arm64` | `f1719755cab857bbbb4961adb1c17e53e76bc6cd428b2e029898be8a209d273c` |
| `xbctl-linux-amd64` | `e10813146d9a643928d039ce2bff222e521cb5b5b0d8205d1684584da2e9aae9` |
| `xbctl-linux-arm64` | `df8b5b69bf58b05acb80a61df40f97f437eae48d25d21982e9bef90a2e4a0531` |
| `install.sh` | `9b685f508ad44ca179914175595fd3bd1c32f1c40ba617177e7807e41317e7ba` |
| `SHA256SUMS` | `838546fd99216921fdbdabdc8bbb1dfba96d5b603bd0c268eb04ba136b3cc61d` |

`v1.13-yz.11` Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `a6228fdd6e41f3753934635165a221405ad841cfabf1c3d6558f120df131b92b` |
| `xboard-node-linux-arm64` | `4874ba28d26cf5f12a0c18cbbab02f440b32218ff173c5a0ec8696fa4bb7e6bf` |
| `xbctl-linux-amd64` | `a1aa15df6f2f23692227d09e2b2bd17665feea0147f7f6157a83477422bb5fb0` |
| `xbctl-linux-arm64` | `cb506202d724a55929e7b9ecbbf31e0ad29e008649c0850d158bb08823e1de08` |
| `install.sh` | `9b685f508ad44ca179914175595fd3bd1c32f1c40ba617177e7807e41317e7ba` |
| `SHA256SUMS` | `9a657fd90efb1d0ab4122d1562902f2aa0a99e57138d86c25630d35823b38170` |

`v1.13-yz.10` Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `ef103c4de2ec4d5e50785491897ccbf0b6c77be5b85c9011f8703aa2d9df333d` |
| `xboard-node-linux-arm64` | `7c0bb626d775eac127ca5e0fce8a7d7381417df61af6fb0471cc2b60a1f54a36` |
| `xbctl-linux-amd64` | `368ce32546c3e4cd431bf788744cb1ebbf997f57f4123e63671aaf3c5a51a14d` |
| `xbctl-linux-arm64` | `cdcbc9a3c811592c546c2761c07bff0e600d0304aec4829c296fdb2858b3a54f` |
| `install.sh` | `d9e6df2cf7b1cd0441c1d2a74d55a2149ed2f25a18120c530fbb650f89bab431` |
| `SHA256SUMS` | `e3c87d67623b787f6f08ff0372d6aa1201cab3954ee60c1cd1dfee2b20c24bdc` |

`v1.13-yz.9` Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `1f2d6c170aed2479ac365089bc185a1d6baa7714a053f701cbb074e578b14340` |
| `xboard-node-linux-arm64` | `95d0b2ce6810ba326ad1e1b7860a8e9a479039431eee84d29b033e2e91501d01` |
| `xbctl-linux-amd64` | `b6f10695cf20c1db407025233853d41da692c3c896c407aac723a11517dad9d4` |
| `xbctl-linux-arm64` | `47fb158e462c5289bdd02f49ac01248f3bea45c30cca02acf1497c1587ac121f` |
| `install.sh` | `d9e6df2cf7b1cd0441c1d2a74d55a2149ed2f25a18120c530fbb650f89bab431` |
| `SHA256SUMS` | `ce25e451979d2275ed9a9ccc10e15613f6b380afbf8674179dcbd8a7552bc770` |

`v1.13-yz.8` Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `9fef57ea43c0fefc9df516863d6753d7435a4f28a33dc3e9eec0d6c5d5d091f0` |
| `xboard-node-linux-arm64` | `65edb812c6893ca3953927f94635c0d865b57298a9f0405ed93563b26d94eed8` |
| `xbctl-linux-amd64` | `a38964e43a5a6ad76d160f6de955fc9d641e3fef48821c92fa0f53decbe5f705` |
| `xbctl-linux-arm64` | `7986dc8386bb82b49b359afbc5d6a99cf8b5631d6eda9a8531e9f757e16470f0` |
| `install.sh` | `d9e6df2cf7b1cd0441c1d2a74d55a2149ed2f25a18120c530fbb650f89bab431` |
| `SHA256SUMS` | `cd57fa28b929b5512f12cfba35e4eae01348c604ca170d13fd3c4deacddf0194` |

`v1.13-yz.7` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `df82755f05292e989a47fa6b7047586f2ae96c00dec8275c13b45e404f3a1a63` |
| `xboard-node-linux-arm64` | `b40510fe856c6c998ccf0a904e59cbda5093550e21c7874c9d324eed80662822` |
| `xbctl-linux-amd64` | `b127af9b59ed2ae5ee6401398158eb0d930f71f9642066dd18fa8eaa8eb05392` |
| `xbctl-linux-arm64` | `b9509d4be3e84d700f17416c13ca23a33c0d0aaa443481673d96abf07bc0d911` |
| `install.sh` | `d9e6df2cf7b1cd0441c1d2a74d55a2149ed2f25a18120c530fbb650f89bab431` |
| `SHA256SUMS` | `cabf9ed0b3af7bd6e5a12f3bcaec67d493dd63329942c812ff302995ed144c23` |

`v1.13-yz.6` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `97c1603121ed6564b45432098eb2d3470be4f330e236b377aa81f039e6e1aae0` |
| `xboard-node-linux-arm64` | `021c0eb0e1ce4fc2b39be18cb397c35c288bf0657097aefe1bc87c28a4efb84d` |
| `xbctl-linux-amd64` | `18bb034e8c880c3aacc5b214ba5e055b45953c7d073c5e87778b7ee88e0a48d6` |
| `xbctl-linux-arm64` | `d4f5a6771a039e5be02174c21addf936bb2a76c00c8ea24636a42d09e684c3a7` |
| `install.sh` | `d9e6df2cf7b1cd0441c1d2a74d55a2149ed2f25a18120c530fbb650f89bab431` |
| `SHA256SUMS` | `c4955fb132756ef9309dc8ba6fd10b3563b35a5255f479d624272eb302d5ce33` |

`v1.13-yz.5` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `336c1efae66987d32be24c59033abc45f1a0e444679abdcf2948c17ea819495f` |
| `xboard-node-linux-arm64` | `d68001dae1eefbdbc00a99e435debf3316570753a334873b81fe8918b157a766` |
| `xbctl-linux-amd64` | `202eadca74a18995189f9c33e51feddeede921ba985c9eecdafb252591e8ebaa` |
| `xbctl-linux-arm64` | `b05e4e90e9a46dfcbcf4f8386b85e4ba9354e9928bdcc92d6b427330890bbe2e` |
| `install.sh` | `32b0317588421622f4ea24d97ab8a5b813a1c767c0c0e43d9e20fb5f8f977f8e` |
| `SHA256SUMS` | `cab369760d4d299b6a5570fbfb5361c941af40e0b95ad45b58e45b2aa4c81bb2` |

`v1.13-yz.4` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `ba23e86f7e7e331d6d5e21d59c59b3eebd1e521ea82eff2832a5faf74c990a8a` |
| `xboard-node-linux-arm64` | `c2d4db57ae6171d58c7fa1b36f2af9458202333b73c6809a0d9d405735cea86d` |
| `xbctl-linux-amd64` | `5daac44d10a074ab3b12237dc37d2b2a9b5d86f578cfe317f171f86e583c639d` |
| `xbctl-linux-arm64` | `72b84b04961c7344aca16c549f61a8cfe652df44871e29a71fcabb71fec75243` |
| `install.sh` | `32b0317588421622f4ea24d97ab8a5b813a1c767c0c0e43d9e20fb5f8f977f8e` |
| `SHA256SUMS` | `a96778a437a3d20de673c84e63b6e4ad84df3ef610f8f6ed684a5eacd0c36eb0` |

`v1.13-yz.3` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `898bfa76a81bfb71f01a5964d8bef8a8032874507086ccbf00e60975a0a0715e` |
| `xboard-node-linux-arm64` | `ccc53f19466e2c9fcf8afeb8ebe3a2ffc70bff199d928243bc2b078df677fde4` |
| `xbctl-linux-amd64` | `e017b653baf8819ab9cdec416daa04c9a35e9ae03a35f36468c019ec524f02bf` |
| `xbctl-linux-arm64` | `c1b62e0846d49fe0e20527127f772aedb73213990e6352748a2ed53476b84eb5` |
| `install.sh` | `32b0317588421622f4ea24d97ab8a5b813a1c767c0c0e43d9e20fb5f8f977f8e` |
| `SHA256SUMS` | `e1082c8c53d4111709683217d187cb6186e04fc900cfa4da7fc972ecba4d33e1` |

`v1.13-yz.2` 历史 Release 资产校验值：

| 资产 | SHA-256 |
| --- | --- |
| `xboard-node-linux-amd64` | `26ceefd8d190abf46eae64c254fb7a8cda5737f46cabe2980b53c62812aed7ca` |
| `xboard-node-linux-arm64` | `9f4b9b5e5178f36c9708f35399e53857a69a1f2a5908d4184775cbd56a306c88` |
| `xbctl-linux-amd64` | `13b477631bae112134588a184422cb91345bda168fee97987c44a542ece2298f` |
| `xbctl-linux-arm64` | `e0c5eb94288a3d2c4d813fa1bc227d76000e20dae74aac6c6324c0b9ce44f19a` |

发布前至少执行：

```bash
go list -m -json github.com/xtls/xray-core
go test -v -race -count=1 ./...
go build -ldflags "-X main.version=v1.13-yz.11" ./cmd/xboard-node
go build -ldflags "-X main.version=v1.13-yz.11" ./cmd/xbctl
```

安装器和升级器从同一 Node Release 下载 `xboard-node` 和 `xbctl`，并使用该 Release 的 `SHA256SUMS` 校验。面板通过 `releases/latest/download/install.sh` 获取最新正式安装器，安装器再通过 `latest` 解析同一正式 Release；需要回滚时必须传入明确的旧 Node Tag。`.github/workflows/ci.yml` 对固定 `v*` Tag 执行测试、双架构构建、Release 资产上传和多架构镜像发布。只有 Release 记录与六个资产完整、校验值一致且 Docker manifest 包含 `linux/amd64` 和 `linux/arm64` 后，才能把 Tag 视为已发布。Xray fork 的回滚边界由 Node `go.mod` 中记录的 pseudo-version 和对应 fork commit 确定。

## 后续上游同步

同步新的 Xray 预发布 Tag 时：

1. 先记录官方 Tag 和对应 commit，再合并到 YZ fork；
2. 解决冲突时保留 Hysteria2 用户识别、统计、Dispatcher 和限速补丁及其测试；
3. 新上游版本的 fork patch 序列从 `yz.1` 重新开始；
4. 同步更新本文档、`go.mod/go.sum`、Node Release Tag、构建信息和变更说明。
