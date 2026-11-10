# YZ-Agent v2.37.0 发布记录

维护者：P0me1oo。日期：2026-11-10。

[v2.37.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.37.0) 已正式发布并设为 Latest，固定源码 `371e42710335748fa4841c8933e173bcf2ea9832`。配套面板 v2.14.0，功能及兼容边界见[自动解绑](machine-binding-deletion.md)。本次未升级运行节点或发布 Docker 镜像。

## 验证与附件

- Go 1.27.3 / Linux amd64，正式功能标签、`-mod=readonly -race -p 1 -count=1 -timeout=20m ./...` 完整回归：41 个测试包、1,001 个顶层用例通过，无失败或数据竞争。
- 普通回归中五个原生防火墙用例/辅助项随后在独立 rootfs、网络、挂载及进程空间验证；UFW / firewalld × nftables / iptables 四组合共 12 项通过。一项既有随机回环端口范围检查按环境条件跳过。
- 八个依赖兼容包、43 项通过；Mihomo v1.19.31 固定附件校验及两项 WireGuard 联调通过；八组安装器和脚本语法检查通过。
- 防火墙首轮并发运行共用了可变 UFW 配置目录而相互干扰；改为顺序执行后 12 项全部通过，未修改产品或弱化断言，未操作宿主防火墙。
- 分支与标签 Actions 均因账号账单锁定没有启动，步骤数为 0；使用上述本机固定源码验证，不将远端结果视为通过。

Linux amd64/arm64 Agent、xbctl 以 `CGO_ENABLED=0`、`-trimpath -buildvcs=true`、固定版本/时间/提交构建。四份构建元数据的 VCS revision、架构和 `vcs.modified=false` 一致；amd64 实际运行版本一致。Release 共 12 份附件，全部已匿名下载并核对 SHA-256，兼容名称 xboard-node 与对应 Agent 字节一致。详见[核验记录](release-2.37.0-verification.json)。

## 使用与回退

先完成配套面板 v2.14.0 的新增迁移，再通过面板“服务器管理”的“升级”更新 Agent，并根据回报版本确认完成。旧面板仍可运行此 Agent，但不能自动解绑；升级前已删除且没有删除凭据的旧身份仍需人工解绑一次。

回退安装包为 v2.36.2。证书、待补报流量和历史备份保留；已经自动移除的绑定需重新绑定，不能仅通过回退程序恢复。最后一个绑定被删除后生成的等待绑定配置是新版功能，回退旧 Agent 前须先添加有效绑定。

发布后记录提交不移动版本标签，不改变安装包。临时测试数据库、Redis、rootfs、构建和下载文件在核验后清理。
