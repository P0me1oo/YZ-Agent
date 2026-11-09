# YZ-Agent v2.36.1 发布记录

日期：2026-11-09。维护者：P0me1oo。

[v2.36.1](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.36.1) 已正式发布并设为 Latest。固定源码 `70dfd6deb7d52891eaffb5c98eee004364d26840`，包含本次错误日志补全；发布记录提交不移动版本标签或改变安装包来源。本次没有升级运行中的节点或发布 Docker 镜像。

## 功能与兼容

补齐启动失败、机器控制轮询、日志缓存与上传、证书续期及临时限速状态读写失败诊断。中转流量、在线来源、连接快照采样失败按警告输出，正常信息日志等级可见；重复故障限频并脱敏，成功后再次失败立即记录。

日志上传器自身故障只输出本地，不递归入队；损坏缓存保留原文件并在策略确认后报告。启动失败最多用五秒确认全部活动身份的日志策略，隐私保护或策略未知时继续静默。上报接口、固定批次重试、两个核心依赖及原有转发逻辑保持兼容；面板自身日志修复已以 2.12.3 部署修补版交付。详见[日志管理](log-management.md)。

## 验证

Go 1.27.3、Linux amd64，正式构建标签 `with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api`；竞态检查使用临时解包 GCC 14，无系统软件安装。

- 完整 `go test -mod=readonly -race -count=1 -timeout=20m -json -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' ./...` 首轮 985 个顶层用例通过、6 项跳过、1 项失败，39 个有测试包通过。失败为未改动的系统 WG 端口占用恢复检查，不能把首轮称为全部通过。
- 相同提交的该失败用例单独复验通过；系统 WG 整包再重复 10 轮，共 50 项通过。首轮原因未复现，没有删除或弱化断言，也未修改该模块或依赖；发布保留这项验证限制。
- 五项原生防火墙用例/辅助项因需要隔离环境在普通回归跳过，随后在独立 rootfs 与网络、挂载、进程空间完成 UFW / firewalld × nftables / iptables 四组合，共 12 项全部通过。另有一项既有随机端口分配检查按实际条件跳过。
- 8 个依赖兼容包共 43 个顶层用例竞态检查通过；固定 Mihomo v1.19.31 下载校验及 WireGuard 双核心、普通客户端 2 项联调通过。上述检查均未报告数据竞争。
- 安装器版本判断、下载地址、服务管理、健康检查、核心默认值、隐私、目录及名称迁移共 8 组通过；Shell/Python 语法检查通过。
- GitHub Actions [分支运行 42571562699](https://github.com/P0me1oo/YZ-Agent/actions/runs/42571562699) 与[标签运行 42572507992](https://github.com/P0me1oo/YZ-Agent/actions/runs/42572507992) 均因账号账单锁定未启动，执行步骤数为 0。使用本机固定提交验证与构建结果，不将 Actions 描述为通过。

## 安装包与下载

从干净固定提交构建 Linux amd64、arm64，采用 `-mod=readonly -trimpath -buildvcs=true`、`CGO_ENABLED=0`。四份构建信息核对完整 VCS revision、操作系统、架构、入口模块和 `vcs.modified=false`；amd64 Agent/xbctl 实际输出版本与提交一致。

共 12 份附件：两个架构的 Agent、兼容名称 xboard-node、xbctl，安装脚本、四份构建信息和 SHA256SUMS。全部已匿名重新下载，字节数和 SHA-256 与本地及 GitHub 附件摘要一致；校验清单 11 项全部通过，两个兼容名称与各自主程序字节一致。

| 主程序 | SHA-256 |
| --- | --- |
| yz-agent-linux-amd64 | `3ed6bc4ae6433bae457673bb3a931fe135f54b81f86d4fb1c05a5cf5d8e26e00` |
| yz-agent-linux-arm64 | `98bdc4025db6a71c19cbb1a6075813419791b1fcee586f95237cd06449a29739` |

结构化来源、验证与附件明细见[核验记录](release-2.36.1-verification.json)。

## 更新与回退

在面板“服务器管理”选择单台或批量“升级”，版本选择 v2.36.1；按操作结果及节点回报版本确认完成。本次只发布，未提交真实节点升级任务。回退固定到 v2.36.0；隐私策略和原持久化数据沿用既有规则。

本次临时编译器、隔离 rootfs、测试程序和下载副本在发布核验后清理，正式附件保存在 GitHub Release；没有保留额外运行服务。
