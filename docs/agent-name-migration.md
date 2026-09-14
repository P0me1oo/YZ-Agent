# YZ-Agent 名称迁移

适用版本：`v1.14.0`。发布状态、固定来源及产物校验记录见 [兼容矩阵](../YZ_COMPATIBILITY.md)。

## 名称与目录

| 项目 | 新名称 |
| --- | --- |
| 展示名、GitHub 仓库 | `YZ-Agent`、`P0me1oo/YZ-Agent` |
| Linux 程序及正常进程名 | `yz-agent` |
| systemd 服务 | `yz-agent.service` |
| OpenRC 服务、PID 文件、日志 | `yz-agent`、`/run/yz-agent.pid`、`/var/log/yz-agent.log` |
| 配置、凭据、实例数据和安装记录 | `/etc/yz-agent` |
| Docker 入口、工作目录 | `/usr/local/bin/yz-agent`、`/etc/yz-agent` |
| 管理命令 | `yz-agent upgrade`、`yz-agent version`、`yz-agent service status` 等 |
| Go 模块 | `github.com/P0me1oo/YZ-Agent` |
| GHCR 发布目标 | `ghcr.io/p0me1oo/yz-agent` |

`yz-agent` 不带参数显示管理帮助，`yz-agent run -c PATH` 启动节点。旧的 `-c PATH` 启动参数仍可使用。节点和管理功能编译在同一个程序中，新安装不再放置独立的 `xbctl`。

两个核心的配置及面板通信格式沿用原约定。版本报告继续记录 Xray、sing-box 的真实依赖和固定 fork 来源，兼容代码仍包含历史名称。GitHub 通过重命名原仓库保留历史；GHCR 使用新的发布目标，旧镜像与历史 Tag、附件和校验记录保持原名。本地工作目录及现有 Git worktree 路径保持原位置。

2026-09-14 已将原 GitHub 仓库重命名为 [P0me1oo/YZ-Agent](https://github.com/P0me1oo/YZ-Agent)，本地 `origin` 已更新。新旧 API 地址指向同一仓库，默认分支仍为 `dev`；改名核对时 `latest` 为 `v1.13.1`，原有 10 个附件保持可获取。当时新旧 `releases/latest/download/install.sh` 地址下载的内容一致，SHA256 为 `b884dd685a95ec4890a5335c2f742fa8615ef85968cb825d7d1116f3fb5b7491`，与改名前相同。

## 迁移路径

- 新版安装器按已校验的程序版本报告选择安装名称，同时识别历史 `xboard-node` 和开发阶段的 `agent` 安装。安装当前版本时只安装统一的 `yz-agent`，显式选择历史版本时恢复相应程序、管理命令与服务。
- 新版 `yz-agent upgrade` 调用自身内嵌的同版安装器完成安装事务。按目标 Release 的 `SHA256SUMS` 优先选择 `yz-agent-linux-*`，历史版本使用其原附件名。新版本只下载一个程序，通过同分区硬链接交给安装器；父进程持续持有安装锁。
- 旧版 `xbctl` 写死了旧程序名和服务名。因此，旧管理器第一次升级只替换程序和管理器，仍运行旧名称；升级后的新版管理器再次执行升级时才完成改名。也可以由新版安装器的 `upgrade` 流程直接完成迁移，无须重装或重新绑定节点。
- 新版管理器在迁移前继续识别和管理旧服务。名称迁移成功后移除旧程序与管理入口，后续操作使用 `yz-agent`；自定义程序目录会同时更新 `/usr/bin/yz-agent` 入口。
- 新 Release 提供 `yz-agent-linux-amd64/arm64`、内容相同的 `xboard-node-linux-amd64/arm64` 兼容附件，以及供旧升级器使用的 `xbctl-linux-*` 迁移附件，全部分别列入 `SHA256SUMS`。

## 配置目录和防火墙迁移

安装器在停止旧服务后，将 `/etc/xboard-node` 整目录移动到 `/etc/yz-agent`，保留实例数据、证书、备份和规则归属记录。只改写配置中位于原目录下的 `kernel.config_dir`、`kernel.geo_data_dir`、`kernel.custom_config`、证书路径、`firewall.state_dir` 和日志文件路径；凭据、实例标识与目录外的配置值保持原内容。用户自定义核心配置文件内部的路径需要在迁移前单独核对。

上述路径同时覆盖根配置、实例及节点覆盖配置。YAML 共享值在迁移时展开为独立值，避免路径锚点连带改变其他字段；扩展字段中的同名键不参与迁移。没有路径需要改写时保留原始文件字节，循环引用或无法解析的配置会在写入前报错。

事务期间临时保留指向新目录的旧路径链接，保证父升级进程持有同一把锁。健康检查和旧服务清理成功后移除临时链接；失败时把整个目录迁回。历史版本回退也使用该流程，恢复 `/etc/xboard-node` 及对应路径。

防火墙继续使用原配置的实例标识。先按原归属记录清理 `yzboard-node:`、`yz_node_`、`YZH_` 规则，再登记新名称 `yz-agent:`、`yz_agent_`、`YZ_AGENT_`。清理失败不会切换名称，后续启动继续重试；其他实例和手工规则保持原归属。

安装器在服务停止后调用新版程序清理已登记规则，历史版本回退前同样执行；该步骤只读取防火墙配置，不要求面板凭据。若新服务无法停止，或回退前无法清理新规则，则保留当前文件和备份，避免旧版本接手无法识别的规则。

## 失败恢复

迁移先检查同名目标，遇到已存在的目标程序或服务文件则停止。先保存旧配置、管理入口和服务文件，再停止旧服务并启动新服务。健康检查通过后才移除旧服务及旧程序。替换、重启、健康检查或旧服务禁用失败，以及可捕获的中断信号，均进入原有恢复流程。

服务定义使用仓库的标准模板。自行编辑的服务文件会保存在安装备份中，但 systemd drop-in 等外部自定义设置不自动迁移；有自定义设置的部署应在更新前核对。旧 OpenRC 日志保留，不合并或删除。

新旧配置目录同时存在、服务或命令入口同名冲突、配置目录为符号链接或跨文件系统搬迁时，安装器停止迁移。程序目录位于旧配置目录内部时，也需先移到独立位置。无法完成恢复时保留恢复文件供排查。

## 验证范围

`tests/install_name_migration_test.sh` 使用隔离目录和模拟服务管理器，覆盖 systemd/OpenRC 下的名称与整目录迁移、历史版本回退、重复升级、父进程锁、硬链接复用、自启动状态、重启和健康检查失败、旧服务禁用失败、中断、路径冲突及数据保留。原有安装目录测试继续运行，新安装只安装 `yz-agent`。

原 `cmd/xbctl` 的管理代码与测试迁入 `internal/agentcli`，原测试保留。`cmd/yz-agent/command_test.go` 通过子进程验证帮助、版本、配置查询、显式启动、旧启动参数和错误命令。配置路径迁移测试覆盖重复调用、版本回退、别名和错误时保留原文件；防火墙测试覆盖三种带名称的旧后端、失败恢复与其他实例规则保护。

2026-09-14 至 2026-09-15 本轮验证使用 Go `1.26.4`、`windows/amd64`。Bash 测试设置 `MSYS=winsymlinks:sys`，保留符号链接语义。

| 验证项 | 结果 |
| --- | --- |
| 完整 Go 测试 | `go test -mod=readonly -count=1 -tags "with_quic with_utls with_wireguard with_acme with_clash_api" ./...` 通过，包括双核心本地转发测试 |
| 依赖回归 | Makefile 中六个依赖测试包均通过，覆盖 AnyTLS、Shadowsocks、Xray singbridge/Hysteria、sing-box gRPC 和 SS2022 兼容副本 |
| 安装器 | 68 个名称与目录迁移场景、2 个防火墙回退清理检查、20 个自定义目录场景及服务模板、默认内核测试通过 |
| 最终修正复验 | 防火墙回退修正后，配置、防火墙、安装锁、管理器和统一入口测试通过；共享 YAML 值修正后，管理器与统一入口测试再次通过，覆盖共享标量、配置段别名、合并、节点覆盖、重复迁移、回退及错误保留 |
| Linux 交叉构建 | `yz-agent` 与 `xbctl` 的 `linux/amd64`、`linux/arm64` 四个最终构建通过；使用 `CGO_ENABLED=0`、`-mod=readonly -trimpath -buildvcs=true` 和 `v1.14.0` 链接版本 |
| 产物核对 | ELF 架构、Go 版本、主包路径与 VCS 信息符合预期；两个主程序解析到兼容矩阵中的固定核心 fork，模块校验值匹配 `go.sum`；`xbctl` 不链接核心 |
| 内嵌安装器 | 四个产物均包含与本地最终 `install.sh` 逐字节一致的内容，其 SHA256 为 `be62833666a4214975332cbaa501a1374d24b2d9e71711b596424f7455ef6f9a` |
| 静态检查 | Shell/Python 语法、CI YAML 解析与 `git diff --check` 通过 |

上述为发布前的本地验证，基线为 commit `8c17d8a3f7d6ede608921cdd5c359c9406970044`，当时源码尚未提交；四个产物均记录该 `vcs.revision` 和 `vcs.modified=true`。主包分别为 `github.com/P0me1oo/YZ-Agent/cmd/yz-agent`、`github.com/P0me1oo/YZ-Agent/cmd/xbctl`。本地验证产物不作为正式发布附件。

本地验证未执行 `-race`、Docker 镜像构建、真实 Linux 服务或原生防火墙命令验收；安装器与防火墙迁移使用隔离目录和模拟命令验证。正式发布的 Linux CI 结果见兼容矩阵；未连接生产服务器。
