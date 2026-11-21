# OpenRC 远程管理

维护者：P0me1oo。候选版本：v2.43.0。2026-11-21 本地修复，尚未发布或部署。

## 问题与原因

v2.41.0 开始按服务管理器记录的进程编号判断 Agent 是否可远程管理。Alpine 使用 OpenRC 的 `supervise-daemon` 守护 Agent，`/run/yz-agent.pid` 保存守护进程编号，实际 Agent 是另一个子进程。直接将此记录与 Agent 自身编号比较，会把正常运行的服务误判为不可管理，面板据此禁用升级、重启按钮。

SID 56 的只读检查确认：Agent v2.41.0 在线、以 root 运行、服务启动正常，但上报不具备远程管理能力。原检查逻辑与守护进程记录方式不兼容；新回归用例在原判断下失败。

OpenRC 在启动实际子进程时保存其编号，并在退出时清除这条记录。依据为官方的 [守护进程实现](https://github.com/OpenRC/openrc/blob/master/src/supervise-daemon/supervise-daemon.c)及[服务记录读写](https://github.com/OpenRC/openrc/blob/master/src/librc/librc.c)。

## 修复机制

保留服务直接记录 Agent 自身编号的兼容路径。使用守护进程时，要求以下条件同时成立：

- 服务 PID 文件记录的是 Agent 当前的直接父进程。
- OpenRC 的 `/run/openrc/options/yz-agent/child_pid` 记录的是当前 Agent。
- 父进程由当前运行用户持有，其实际可执行文件与已安装的 `supervise-daemon` 是同一文件。
- 进程记录由当前运行用户持有，是不允许组或其他用户写入的普通文件；读取拒绝符号链接、特殊文件、无效或超长编号。

运行入口继续要求 Linux、root、完整的服务管理器及独立执行工具。systemd 沿用服务主进程编号检查，服务重启和升级执行事务继续使用已有流程。

## 验证与恢复边界

验证覆盖正常守护关系、直接记录 Agent 的兼容路径、其他服务或进程、缺失及异常子进程记录、错误的守护程序、文件权限、所有权、符号链接和特殊文件。

2026-11-21 本地验证：Linux amd64、Go 1.27.3，使用已有依赖缓存和临时 Zig 0.15.2 编译器，开启 CGO 与并发检测。正常守护关系用例先在原判断下复现失败，再通过修复后的检查；新增 22 个子场景全部通过。

```sh
go test -mod=readonly -race -p 4 -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./internal/agentcli ./internal/machine -count=1 -timeout 5m
go vet -mod=readonly -p 4 -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./internal/agentcli ./internal/machine
```

两个相关包合计 100 个顶层用例、含子场景 245 项检查通过，没有失败、跳过或数据竞争；静态检查、格式和改动空白检查通过。源码候选基于 `f62a66b1e1fabca8720a5ef0ff88eb82ae0a10c1` 加当前工作区改动。没有运行整仓发布回归或线上修复验证。

源码修复尚未进入正式安装包，线上 SID 56 仍运行 v2.41.0。首次恢复需要将包含修复的 Agent 更新到该机器，并通过新鲜的控制心跳确认管理能力恢复；之后继续使用面板「服务器管理」的升级入口。服务器更新按本次明确授权执行。
