# YZ-Agent 兼容矩阵



## v2.28.3 设备空闲许可归还与正式发布（2026-10-30）

- [Release v2.28.3](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.28.3) 已发布，固定来源 `361a57ce8625e9d7bdb2d8fec79af7f2291c3b4e`，最终配套面板 `1.111.8`。面板 `1.111.7` 为保留的中间标签，未完成发布；协议初稿中的该版本号由本节更新。
- [发布流水线](https://github.com/P0me1oo/YZ-Agent/actions/runs/41009580982) 全部成功：Go `1.27.1` 完整竞态、两个核心和兼容模块、Mihomo WireGuard 客户端、安装器，以及 UFW/firewalld × nftables/iptables 四组原生防火墙均通过；两架构安装包及镜像从同一固定提交构建。
- 12 份原始附件已匿名下载；GitHub 大小和 SHA-256 均匹配，`SHA256SUMS` 的 11 项全部通过。两个架构的旧名称兼容附件与 `yz-agent` 字节一致；ELF 架构、实际构建元数据、完整提交、`CGO_ENABLED=0`、`vcs.modified=false` 及四份构建信息核对通过。amd64 的 `yz-agent`、`xbctl` 已独立执行版本入口；两架构镜像版本由流水线实际运行核对。
- Agent SHA-256：amd64 `74caa7b8c69643b032fbb9b43bcd39e7e9e38dcb23f82fb620fba0ae3eaab6f2`；arm64 `ca64c43be3f0a1955f82827127ca931c5b5ecbfc66755a8c2df2487f96311428`。固定依赖仍为 YZ-sing-box `v1.15.1` 和 YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`。
- 镜像版本标签、完整提交标签和 `latest` 均指向 `sha256:98e6e0f6782119a487e4086cb6dda6496be84ccfa97cc42f243b911430abf1c8`；两架构 OCI revision 与来源提交一致。已上传 [verification.json](https://github.com/P0me1oo/YZ-Agent/releases/download/v2.28.3/verification.json) 和校验文件，共 14 份附件。
- 在线空闲许可在确认无连接和在途申请后停用并归还，归还后重连需重新申请；断联未归还、未撤销的许可仍保持原固定一小时期限。迟到许可、新旧会话、权限恢复、真实关闭及多节点共享继续受保护。未部署生产或修改生产数据；无到期保证的旧协议残留仍需真实关闭证据。

## v2.28.2 正式发布核验（2026-10-29）

- [Release v2.28.2](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.28.2) 已发布，固定来源 `698e7df5712e848e2a45eb835ad367c55f948c4d`，配套面板 `1.111.3`。下节开发阶段记录保留；本节为正式交付依据。
- [发布流程](https://github.com/P0me1oo/YZ-Agent/actions/runs/40890125681) 的 Linux / Go `1.27.1` 完整竞态、底层兼容模块、Mihomo、安装器及 UFW/firewalld × nftables/iptables 四组原生防火墙检查全部通过。没有重复运行同一源码的完整回归；两个架构程序与镜像从同一固定提交构建。
- 12 份原始附件已匿名下载，大小与 GitHub 摘要一致，`SHA256SUMS` 的 11 项全部匹配；同架构的 `yz-agent` 与旧名称兼容附件字节一致。实际 ELF 架构、四份构建信息中的完整提交、`CGO_ENABLED=0`、`vcs.modified=false` 和固定核心来源均核对通过。程序版本由发布工作流实际运行核对：amd64 程序和两个架构镜像均为 `v2.28.2`，不是以 Go 自动生成的模块版本代替程序版本。
- Agent SHA-256：amd64 `1e089676dcf5af3fbc5e15294e13be20f9d3ac8314307cca705e307c17ba5159`；arm64 `6001350d64e0c953528a2a91f54072b0383ed46812305883c11dae23133e0158`。固定依赖仍为 YZ-sing-box `v1.15.1`、YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`。
- 镜像 `ghcr.io/p0me1oo/yz-agent:v2.28.2`、完整提交标签和 `latest` 指向同一 manifest：`sha256:2277fe9500c675990e7c7008cb4d7c950b368fcdf56d0f8126051f2307f95f07`。`linux/amd64`、`linux/arm64` 的 OCI revision 均与固定来源一致，可匿名获取；详情见 [verification.json](https://github.com/P0me1oo/YZ-Agent/releases/download/v2.28.2/verification.json) 与对应校验文件。
- 先更新面板，再更新 Node。上一版 `v2.28.1` / `0525180ef59c1856f7412a9f93e7d7c251dd0ac8` 及镜像摘要 `sha256:27b441355a7c807d626d57987554a6be3f3e5dff9e27f419d5e2f929cd34f7fd` 已核对可获取；回退会失去本次撤销恢复修复。本次未连接或更新生产服务器，生产故障是否完全由此造成仍未最终确认。

## v2.28.2 设备许可撤销恢复修复（2026-10-29，本地未发布）

- 基于干净的 `dev` / `016e604661c42c4445f6b74c7000d220b604e64a`，配套面板 `1.111.3`。两个核心、Go 依赖、管理端不变；保持原共享许可编号、设备名额、真实关闭及一小时断联期限。
- 撤销记录保留请求边界和已处理的撤销序号。重新授权必须在撤销后发起，并由面板返回的 `confirmed_sequence` 覆盖该请求；响应抵达顺序和墙上时钟都不能单独解除撤销。用户移除阻止未知许可的在途申请，旧会话响应不影响新会话，旧空列表继续不能删除新授权。
- Go `1.27.0` / Windows amd64 的 `internal/devicegate`、`internal/panel` 整包竞态检查通过；补齐旧会话保护后设备管理整包竞态再次通过。新增七项顶层恢复测试，包含六组撤销方式/恢复入口、三组缺失/过旧/矛盾核验，以及真实关闭、断联、用户移除、会话重建、多节点共享和响应乱序；HTTP、长连接及确认丢失转 HTTP 均验证核验序号传递。
- 完整功能标签的 Xray、sing-box、普通 WireGuard、服务及控制通道专项竞态检查全部通过。覆盖设备准入和真实关闭、到期计数释放、机器额度保护、配置失效后恢复、双核心 JSON/YAML 启动与重复重载的数据收发；没有扩大到整库回归。补强后的“已获重新授权但旧连接未释放”用例单独通过。
- Linux amd64 完整功能标签构建成功，版本入口为 `v2.28.2`，`CGO_ENABLED=0`、`-trimpath`、`vcs.modified=true`，VCS revision 为上述基线；这是未提交工作区构建，不是正式附件。实际固定来源为 YZ-sing-box `v1.15.1`、YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`，SHA-256 为 `1cfee3629486238d8fa01369c8fdf8325b57af9bc61e9b933713386f392aa46e`。
- 本机 WSL 启动虚拟机超时（`Wsl/Service/CreateInstance/CreateVm/0x800705b4`），Linux 产物未完成实际版本执行或 Linux 运行验证；以上双核心实际运行结果来自 Windows。未重启或修改 WSL 服务。
- 先更新面板，再更新相关 Agent；旧面板缺少核验序号时不能提前恢复已撤销原编号。未运行全库回归或服务器测试，未提交、推送、发布。原诊断只确认本地缺陷，生产故障的最终因果仍未确认。结果与本地构建位于 `D:/tmp/yz-lease-recovery-20261029/`，修复语义见[设备许可](docs/device-counting.md)。

## v2.28.1 加密配置启动与文件豁免修复（2026-10-29，已发布）

- 基于 `dev` / `ed15db8bfdd1fbfef253ecbee87167830efe9d55` 及已有未发布改动，配套面板 `1.111.1`、管理端 `0.81.0`。包含此前隐私保护及跨节点许可功能，YZ-sing-box `v1.15.1`、YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987` 固定依赖不变。
- 自定义配置的启动与重载校验统一通过解密读取；加密豁免按已登记主配置及同目录启动文件的完整路径判断，不再豁免任意目录的同名文件。
- Go `1.27.0` / Windows amd64 的 `internal/model`、`internal/privacy`、`internal/config` 整包检查通过。新增用例覆盖双核心 JSON/YAML 校验、解密后重复标签拒绝、未关联密文拒绝与恢复、主配置自定义名称、同名文件迁移及重复写入、隐私配置重载。
- 双核心实际运行用例 `TestEncryptedCustomConfigStartsAndReloadsBothCores` 的 Xray、sing-box × JSON、YAML 四组场景通过；每组验证加密的 `config.yml`、启动、两次重载和每次运行后的本地 TCP 数据收发。
- 正式来源：`0525180ef59c1856f7412a9f93e7d7c251dd0ac8`，Tag 为 `v2.28.1`。[发布流程](https://github.com/P0me1oo/YZ-Agent/actions/runs/40735627751) 的完整竞态、兼容模块、Mihomo、安装器和四组原生防火墙检查通过，Linux amd64/arm64 的程序、兼容入口、管理工具均构建成功；构建信息与下载附件一致，`CGO_ENABLED=0`、`vcs.modified=false`，核心固定来源已核对。
- 镜像为 `ghcr.io/p0me1oo/yz-agent:v2.28.1`，完整提交标签及 `latest` 指向同一 manifest：`sha256:27b441355a7c807d626d57987554a6be3f3e5dff9e27f419d5e2f929cd34f7fd`。两个架构的 OCI revision 与上述来源一致，可匿名获取。
- 12 份原始附件已下载并核对 SHA-256、GitHub 摘要及大小，amd64 程序实际版本和来源已核对。Agent SHA-256：amd64 `81241a7a839f677ed3ec0639d6704fc654295058fd8e9651a7efdba79eb774da`，arm64 `0fcf80b63c1cf71360da85c78224d01f090fafec3a23453eaa7264b14ab63f9c`。完整记录见 [verification.json](https://github.com/P0me1oo/YZ-Agent/releases/download/v2.28.1/verification.json) 和对应校验附件。
- 先更新配套面板 `1.111.1`，再更新 Node。上一正式版本为 `v2.26.0` / `e1068a733bc5b03cc30e119f9db2202bc3689e93`；回退旧版须沿用受支持的维护流程，先转换加密数据。未连接或更新生产服务器。

## v2.28.0 跨节点许可与断联期限（2026-10-28，本地未发布）

- 基于 `dev` / `ed15db8bfdd1fbfef253ecbee87167830efe9d55` 及已有未提交修改，配套面板 `1.111.0`、管理端 `0.81.0`。保留已有隐私保护功能，不修改两个核心 fork 或固定依赖。
- 缓存有效许可供跨节点复用和空闲重连，只有面板重新核验才能续期；本地使用单调时钟并扣除响应耗时，最长一小时。新连接、重试和重启不增加期限，到期或已知撤销时独立发起连接关闭；慢关闭不拖住其他连接。各账号申请独立，迟到空快照不删除新许可，详见[设备许可](docs/device-counting.md)。
- 新增 14 项顶层许可缓存、通信编码和双核心到期用例。Windows amd64 的设备管理及面板通信整包 `-race` 检查通过，覆盖本地复用、未知来源拒绝、固定期限、网络阻塞、慢关闭、撤销优先、权限移除、重启、响应乱序和独立申请。双核心、普通 WireGuard、服务及控制通道的原设备准入、真实关闭、配置快照、重载恢复专项通过，最终关闭逻辑的 WireGuard 与服务专项再次通过。
- 最终 Linux amd64 的 Xray、sing-box 设备替换和到期关闭专项通过，实际验证 TCP、UDP 连接关闭及计数释放。完整功能标签 `with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api` 构建通过，`CGO_ENABLED=0`、`-trimpath`。程序版本 `v2.28.0`，VCS revision 为上述基线，`vcs.modified=true`；本地程序内构建时间与提交标识为 `unknown`，不是正式发布附件。
- 固定依赖仍为 YZ-sing-box `v1.15.1`、YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`。最终 Linux amd64 二进制 SHA256：`f2bfaddd2a203862db163cb854edeb6bb60881488e09080d9b776e9dfbbfa564`。源码校验值、Linux 日志和核验记录位于 `D:/codex-tmp/yz-device-lease-20261028/`。
- 未运行 Node 完整回归，未连接服务器，未提交、推送或发布。先更新面板，再更新全部涉及设备计数的 Node；旧组合不能视为已具备固定断联期限。

## v2.27.0 面板密钥与本地加密（2026-10-28，本地未发布）

- 基于 `dev` / `ed15db8bfdd1fbfef253ecbee87167830efe9d55` 及前一轮本地隐私保护修改，配套面板 `1.110.0`、管理端 `0.81.0`。新增按机器认证的文件密钥接口 v1，不修改两个核心、既有流量接口或计费格式。
- 保留永久停用日志；由面板派生每台机器的 AES-256-GCM 密钥，进程内保存，重启自动重新领取。按实例保护敏感配置、受管理证书和待上报状态；保留启动凭据和必要运维摘要，支持密钥轮换、重复迁移、并发修改保护、独立恢复、目录迁移与旧版回退。边界和面板密钥备份要求见[使用说明](docs/log-privacy.md)。
- Go `1.27.0` 的 Windows 本地配置、管理工具及加密专项通过。Linux amd64：文件保护 5 项、配置 45 项、管理工具 83 项、访问记录 5 项、运行日志队列 4 项、机器流量 14 项、证书 7 项、服务恢复与报告 23 项、机器协调 8 项、主程序 6 项、双核心启动与重载 8 项顶层用例通过，共 208 项；覆盖新进程领取密钥、面板故障隔离与恢复、旧备份恢复、凭据轮换、篡改拒绝及流量和重试编号保持。
- 机器地址回报原用例首次在 WSL 仅有 IPv4 的 localhost 映射下失败；只在隔离挂载命名空间中补齐 IPv6 映射后，原用例通过。未改动系统 hosts 或相关源码，未跳过或弱化测试。
- 安装器隐私检查覆盖标准输入、旧版本、改名程序降级，以及无法解密时保留当前程序并尝试重启；20 项自定义目录、72 项名称迁移和失败恢复、默认内核及安装器语法检查通过。隐私检查已纳入 CI 和 Makefile 测试入口。
- Linux amd64 完整标签 `with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api` 构建通过，`CGO_ENABLED=0`、`-trimpath`；版本为 `v2.27.0`。固定依赖仍为 YZ-sing-box `v1.15.1`、YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`，VCS revision 为上述基线，`vcs.modified=true`。本地默认构建时间和程序内提交标识为 `unknown`，不是正式发布附件。
- 未执行完整协议、竞态或真实服务器回归；未提交、推送、发布或连接服务器。备份、日志和本地构建保存在 `D:/codex-tmp/yz-private-storage-20261028-083039/`。
## v2.26.1 隐私保护加固（2026-10-28，本地未发布）

- 基于 `dev` / `ed15db8bfdd1fbfef253ecbee87167830efe9d55`，配套本地面板 `1.109.1`、管理端 `0.80.1`；两个核心固定依赖及通信格式不变。原永久停用日志设置更名为“隐私保护模式”，已有策略及不可恢复行为继续沿用。
- 配置、凭据及安装记录保存前收紧权限为 `0600`，覆盖已有文件、绑定增删及配置迁移；保留符号链接维护方式。安装和失败恢复同步应用，默认安装目录保留 `0700`。新版配置生成凭据经标准输入传递，历史版本按能力探测保留旧参数方式。
- Go `1.27.0` / Linux amd64 的管理工具整包 82 项顶层测试通过，包括五类保存路径、重复调用、符号链接、文件写入错误、凭据输入错误及兼容能力探测。安装器凭据传递及历史版本兼容检查、默认内核检查、20 项目录场景、72 项名称迁移与恢复场景通过；安装器语法检查通过，新增凭据检查已纳入现有持续集成。
- Linux amd64 的完整功能标签构建通过：`with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api`，`CGO_ENABLED=0`、`-trimpath`。实际版本查询为 `v2.26.1`，安装凭据输入能力探测成功；实际解析仍为 YZ-sing-box `v1.15.1` 和 YZ-Xray-core `v0.0.0-20261018151951-fb2f909b7987`。VCS revision 为基线 `ed15db8bfdd1fbfef253ecbee87167830efe9d55`，`vcs.modified=true`；构建时间及程序内提交标识仍为本地默认的 `unknown`，没有正式发布附件。
- 未运行协议、核心完整回归或真实服务器测试，未提交、推送或发布。备份、测试日志和本地构建位于 `D:/codex-tmp/yz-privacy-20261028-072922/`。保护范围及历史版本限制见[使用说明](docs/log-privacy.md)。
