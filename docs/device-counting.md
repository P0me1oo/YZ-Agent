# 设备计数与设备快照续期

## v2.4.1 修复范围

设备按来源公网地址计数，IPv4 按单个地址，IPv6 按 `/64` 网段合并，并排除名单内来源。连接登记后占用名额，同一来源的最后一条连接关闭后移除。设备限制不等于物理终端识别，切换网络时旧连接未释放仍可能短暂占用名额。

Xray 此前分别保存不限设备用户和有限制用户的连接。用户修改上限时只更新限制值，没有合并两类记录；新连接可能漏掉旧来源，关闭时也可能查错记录，造成所有连接已关闭但设备和连接数仍有残留。

本版使用统一的每用户连接记录。设备准入、关闭和上报读取同一份来源引用计数；每个用户独立加锁，检查和登记保持连续，修改限制和身份映射时使用读写锁协调。限制变更不主动断开已有连接，新来源按新上限判断；已有来源的连接关闭后正确扣减，计数不会因上限变化而迁移。

## 跨节点设备续期

面板 `1.39.1` 对未变化的完整设备名单每十秒重新发送。Node 接受同一有效版本并刷新收到时间；过期版本、已退出的旧运行代次以及缺失名单不续期，明确的空名单清空设备并续期。超过三十五秒没有有效快照时继续清除跨节点设备；恢复后可重新接收同一有效版本。

本版没有延长失联阈值，也不把正常负载或状态确认当成设备名单续期。两种核心共用设备同步路径，原有核心内快照有效期作为兼容保护保留。HTTP 兜底仍按原周期拉取完整状态。

## 配套与验证

- 完整修复配套面板 `1.39.1`、Node `v2.4.1`；部署时先更新面板，再更新 Node，用户无需强制刷新或重新导入订阅。
- 旧 Node 已支持相同有效版本续期，面板可以先行修复稳定名单失效。Node 单独升级不能修复旧面板不续期。
- 不修改两个核心 fork、不调整固定依赖、不修改管理端、通信格式或可靠流量结算。
- 回归覆盖不限与有限制之间往返切换、旧来源仍在线时的新来源拒绝、关闭归零、并发变更，以及相同版本续期、旧包拒绝、空名单、失联过期和恢复。验证结果见兼容矩阵。
- 未使用真实服务器或生产身份，发布状态与产物来源以兼容矩阵为准。

## 本地检查命令与环境

本次使用 Go 1.27.0、Windows amd64 和下列完整功能标签。首轮普通全库检查把时限设为五分钟，在 sing-box 包超时；单独中转复测通过后，按仓库 Makefile 原有的二十分钟时限重新执行。sing-box 包复跑通过，用时约九分半；复跑仅调整命令时限，未修改测试文件或筛选用例。全库最终结果见兼容矩阵。

```text
go test -p 1 -mod=readonly -tags "with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api" ./... -count=1 -timeout 5m
go test -p 1 -mod=readonly -tags "with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api" ./... -count=1 -timeout 20m
go test -race -p 1 -mod=readonly -tags "with_quic,with_utls,with_wireguard,with_gvisor,with_acme,with_clash_api" ./internal/kernel/xray ./internal/service ./internal/controlplane ./internal/panel ./internal/machine -count=1 -timeout 5m
```

五包竞态检查开启 CGO 并使用 D 盘已有的 LLVM MinGW 编译器，全部通过。Linux amd64/arm64 程序使用相同功能标签交叉构建，关闭 CGO；产物里的核心依赖、架构、源码标识及 SHA256 记录在兼容矩阵。本地未提交工作区构建会标记 `vcs.modified=true`，不等同正式发布产物或 Linux 实机验证。
