# 自定义程序目录

本功能从 `v1.13-yz.23` 起提供，修改基线为 `1d74d19cbb738e6fcde466361c24a186b8deec60`。当前源码名称为 YZ-Agent；本功能只涉及节点的安装和管理流程，面板、主控通信与内核依赖保持原有约定。

## 安装位置与兼容行为

`install.sh install` 和 `install.sh upgrade` 接受 `--bin-dir`。该目录存放统一的 `yz-agent` 程序及本次升级的大文件，历史版本分别使用 `xboard-node` 和 `xbctl`。改名迁移后，配置、凭据和实例数据位于 `/etc/yz-agent`，OpenRC 日志为 `/var/log/yz-agent.log`，旧日志保留。目录记录文件为 `/etc/yz-agent/bin-dir`，内容是一行绝对路径。安装和更新命令见 [README](../README.md#升级)。

无记录的旧安装继续使用 `/usr/local/bin`。已有记录但内容无效时停止操作，不能退回默认目录后误改其他文件。状态查询、绑定变更后的服务文件生成、升级及卸载均沿用同一目录；卸载不递归删除用户指定的程序目录。

路径只支持字母、数字和 `/._-`，拒绝空格、控制字符、相对路径、根目录以及中间的重复斜线、`.` 和 `..`。程序分区必须允许执行并支持硬链接。systemd 使用 `RequiresMountsFor`，OpenRC 增加 `localmount` 依赖，保证重启后先挂载再启动。

## 迁移、升级与恢复

安装器先在目标程序分区下载并验证二进制，再备份小体积配置和服务文件。同目录升级使用硬链接保留原二进制；迁移到其他目录时，原二进制留在原位置，直到新服务通过检查。大文件的最终替换均在目标分区内重命名，不依赖跨分区重命名。

`yz-agent upgrade` 也在已登记的程序目录创建独立临时目录，并使用硬链接保留旧程序。正常升级需要保存当前版与新版两套程序内容，不再复制第三套备份。校验仍使用目标 Release 的 `SHA256SUMS`，并执行目标程序的版本检查；回退历史版本时同时校验独立的节点和管理工具。

安装器与 `yz-agent` 共用安装目录中的 `.install-lock`；新版位置为 `/etc/yz-agent/.install-lock`，旧目录迁移期间保留同一把锁。替换前的错误不改变运行文件；替换或重启失败会恢复原程序，安装器同时恢复配置目录、目录记录、服务文件和管理入口。安装器收到可捕获的中断信号时执行相同的恢复流程。恢复失败时保留恢复文件并报告目录。

进程被强制终止或机器断电时不能保证自动完成恢复。此时保留的锁和临时目录应先由操作者核对；确认没有仍在执行的安装操作，并确认程序、服务和目录记录一致后，再处理锁或重试。旧安装残留的 `.new`、`.bak` 文件不属于本次临时目录，不自动删除。

自定义目录下的新版安装器和升级器会探测目标管理入口是否支持目录查询。若目标旧版本不支持，则在替换前拒绝；需要回退旧版时，应先通过新安装器迁回 `/usr/local/bin`。

当前版本的测试与构建结果见 [兼容矩阵](../YZ_COMPATIBILITY.md)。以下保留 `v1.13-yz.23` 的原始验证记录。

## 本地验证

验证使用隔离目录和专用模拟程序，不读取生产配置，不连接或修改服务器。

2026-09-09 已在 Windows、Go 1.26.4 和 Git Bash 下完成下列检查。Go 测试与 `go vet` 通过；安装器最终 20 个场景及已有服务文件测试通过。迁移测试的配置和原程序位于 C 卷，目标程序目录位于 F 卷，测试通过设备编号确认两者属于不同文件系统；shell 符号链接使用 Git Bash 的 `winsymlinks:sys` 模式。

- Go：目录记录兼容与校验、安装锁、两种服务文件、升级时的实际硬链接身份、重复升级、下载空间不足、校验或版本检查失败、替换失败、服务重启失败及恢复文件保留。
- 安装器：20 个场景，覆盖新安装、迁移、已保存目录、卸载、第二个程序替换失败、迁移或同目录重启失败、下载失败、备份失败、目标冲突、无效目录记录、管理入口位于旧目录或新目录，以及服务重启和两个文件替换瞬间的中断恢复。
- shell 检查：安装器与测试脚本语法，以及已有 systemd/OpenRC 服务文件测试。

复现相关检查：

```bash
go test -mod=readonly ./cmd/xbctl ./internal/config ./internal/buildinfo
bash -n install.sh tests/install_paths_test.sh tests/install_service_manager_test.sh
bash tests/install_service_manager_test.sh
bash tests/install_paths_test.sh
```

`YZ_TEST_BIN_PARENT` 可以指定另一个允许执行且可写的文件系统中的测试目录父路径，用于验证跨文件系统迁移；测试只创建并清理自身的临时子目录。Windows Git Bash 需要 `MSYS=winsymlinks:sys` 才能在没有原生符号链接权限时验证 shell 的链接行为。

`xbctl` 的 `linux/amd64` 与 `linux/arm64` 交叉编译均通过，使用 `CGO_ENABLED=0`、`-mod=readonly`、`-trimpath` 和 `-buildvcs=true`。构建版本参数为 `v1.13-yz.23-dev`；两份产物的构建信息均确认来源为上述修改基线、`vcs.modified=true`，对应未提交工作区。模块解析未新增依赖；这些临时产物仅用于构建检查，检查后清理，不作为正式发布。

本地检查不能替代真实 Linux 服务启动、机器重启及正式发布构建的验证；正式 CI 已接入新增安装器测试，发布构建与产物验证结果见下文。本次未进行生产迁移。

## 发布前 Linux 检查

发布前 Linux CI 发现同目录升级收到终止信号时重复执行回滚。[失败场景日志](https://github.com/P0me1oo/YZboard-Node/actions/runs/34268427384) 确认：信号处理已恢复旧文件，随后 Bash 又触发 `ERR`，再次恢复时删除了已经归位的程序。错误和信号处理入口现均关闭 `ERR` 回调，原有中断用例继续保留；修复后本地跨文件系统测试和 Linux 20 个安装器场景均通过。

修复后的[第一次全量检查](https://github.com/P0me1oo/YZboard-Node/actions/runs/34269113262/attempts/1) 在 `internal/kernel/singbox` 包的 `TestSingBoxRelayVLESSTransports/httpupgrade/singbox` 用例遇到一次 `EOF`，位置为 `relay_runtime_test.go:195`。执行命令为 `make test` 中的 `go test -mod=readonly -v -race -count=1 -tags "with_quic with_utls with_wireguard with_acme with_clash_api" ./...`。该中转代码和内核依赖与 `v1.13-yz.22` 一致，未在本次修改中调整。

使用同一提交和全部检查[重跑](https://github.com/P0me1oo/YZboard-Node/actions/runs/34269113262/attempts/2) 后通过；Windows 下单独重复该用例 20 轮也通过：

```bash
go test -mod=readonly -count=20 \
  -tags 'with_quic with_utls with_wireguard with_acme with_clash_api' \
  -run '^TestSingBoxRelayVLESSTransports$/^httpupgrade$/^singbox$' \
  ./internal/kernel/singbox
```

这次 `EOF` 的根因未确认，未据此修改内核或删除、跳过、放宽测试。后续正式发布从同一源码执行的完整检查也通过。

## 正式发布验证

`v1.13-yz.23` 已于新加坡时间 2026-09-09 发布，固定源码为 `1944c8eb7982c4f6156d6adff8e8a734cdc1813b`。
[正式 CI](https://github.com/P0me1oo/YZboard-Node/actions/runs/34270886003) 完成 Linux `make test`：690 项 Go 测试及子测试、20 个安装器场景和已有服务文件测试通过，无失败、无跳过、无数据竞争报告。

Node 与 xbctl 的 Linux amd64、arm64 安装包和双架构镜像均已构建；10 个 [Release 附件](https://github.com/P0me1oo/YZboard-Node/releases/tag/v1.13-yz.23) 已下载，逐项核对 GitHub 摘要、`SHA256SUMS` 和实际构建信息。四个二进制的来源、目标架构、Go 1.26.4、`CGO_ENABLED=0` 与 `vcs.modified=false` 均符合正式构建要求。CI 已运行 amd64 的两个程序及双架构 Node 镜像，核对版本和来源；arm64 镜像版本检查通过 QEMU 执行。

镜像 `ghcr.io/p0me1oo/yzboard-node:v1.13-yz.23`、完整提交标签与 `latest` 均指向 `sha256:e041bab08ea982bd205cbe72e93509175de79cbb2c827f73e80d7c7ea76810c2`。两个平台的 OCI 来源与版本一致，匿名读取通过。各附件校验值、平台 manifest 和 `v1.13-yz.22` 回退引用见 [兼容矩阵](../YZ_COMPATIBILITY.md)。

本版 amd64 两个程序合计约 78.24 MiB，同目录升级的两套程序内容约 156.48 MiB；配置备份、日志和其他文件另计。安装器在目标程序分区暂存大文件，配置备份和目录记录仍需少量根分区空间。

本次安装器验证使用隔离目录和模拟服务，没有执行真实服务器迁移、Linux 实机服务启动与系统重启挂载验收，也没有进行 arm64 实际转发。发布结果以独立文档提交记录，正式 Tag 保持不变。
