# 运行日志保存与上传优化

维护者：P0me1oo。日期：2026-11-17。候选 Agent `v2.40.0`，配套面板 `2.23.0`；本次没有提交、发布或更新服务器。

## 改动与兼容

1. 待上报运行日志缓存按修订号保存。新增记录、组成或确认批次、文件变化及保护策略切换才重新落盘；未变化缓存只检查策略和文件元数据。保存成功仅确认本次快照的修订号，保存期间新加入的记录仍会在下一轮保存；失败不会把未保存记录标记为完成。固定批次继续在发送前持久保存、确认后清除，重试不改编号。
2. `runtime.log.enc` 复用文件句柄，每次追加仍核对文件身份、长度、修改时间和权限。外部替换时关闭旧句柄、打开并校验新文件；同长度改写也会重新校验。轮转、隐私关闭、实例身份替换、重载和正常退出回收句柄。保持逐条 AES-256-GCM、原文件格式、20 MiB 轮转与历史密钥读取，不增加异步写入缓冲。
3. 面板在原日志握手能力中新增可选最低上传等级，当前为警告。新 Agent 在脱敏及入队前筛选较低等级，握手前或恢复后尚未组成批次的低等级记录也会筛选；已经固定的待确认批次保持原位置。Xray 崩溃事件仍保留。本地日志按原等级记录，隐私机器继续本地加密。旧 Agent 忽略该字段，新 Agent 面对未声明或未知等级的面板沿用原上传范围。

保护仍只作用于程序管理的文件，磁盘失败、损坏和缺少密钥不会回退到明文输出。没有新增数据库迁移、日志设置、依赖或核心补丁。面板保留期、入库筛选和通知防重规则保持，使用及恢复边界见[隐私模式](log-privacy.md)。

## 性能对照

环境为本机 Go 1.27.3、Linux amd64、AMD Ryzen 9 9950X，Go 可用并行度为 6，测试文件位于 `/tmp` 的内存文件系统。优化前后使用相同夹具，各重复四次，取耗时中位数；优化前每次计时 200 ms，优化后 300 ms。本次任务的回归和编译与最终性能计时分开执行，最初与回归同时启动的一轮结果已舍弃。

| 路径 | 优化前 | 优化后 | 耗时变化 | 每次内存分配次数 |
| --- | ---: | ---: | ---: | ---: |
| 单条 128 字节加密日志 | 5.075 μs | 2.349 μs | 减少约 54% | 15 → 12 |
| 单条 1024 字节加密日志 | 7.171 μs | 5.201 μs | 减少约 28% | 15 → 12 |
| 未变化普通日志缓存保存检查 | 16.201 μs | 1.033 μs | 减少约 94% | 27 → 5 |
| 未变化加密日志缓存保存检查 | 20.880 μs | 1.029 μs | 减少约 95% | 42 → 5 |

普通信息发布分支的同版对照：订阅后再忽略等级仍执行脱敏，中位耗时 30.693 μs、30 次分配；提前筛选后约 25.4 ns、零分配。此项仅测发布分支，未计入日志文本生成、本地保存、网络及面板处理，不能将其比例作为整条日志链路的收益。

基准可重复运行：

```sh
CGO_ENABLED=0 go test -mod=readonly -run '^$' \
  -bench 'Benchmark(LogWriter|CollectorUnchangedSave|PublishInfoFiltering)$' \
  -benchmem -benchtime=300ms -count=4 \
  ./internal/privacy ./internal/agentlog ./internal/nlog
```

这些是共享开发机上的局部微基准，减少的是日志处理自身的开销，不代表整机 CPU 或生产请求延迟同比下降。没有切换生产隐私策略做对照，也没有测量生产磁盘。

## 验证

四个受影响包的完整竞态回归通过：文件保护、日志出口、运行日志采集、面板客户端。覆盖未变化缓存不重写、失败重试、并发新增及并发保存、反复隐私开关、固定批次位置、外部文件替换、同长度损坏、文件权限、句柄回收、旧面板和未知等级回退、本地全部等级保留及上传脱敏。

最终文件保护 14 项、日志出口 9 项和启动入口 9 项全部通过；双核心及配置相关 13 项通过，均启用竞态检测，核心相关检查使用正式功能标签。没有失败或数据竞争。命令范围如下：

```sh
go test -mod=readonly -race -count=1 -timeout 5m \
  ./internal/privacy ./internal/nlog ./internal/agentlog ./internal/panel

go test -mod=readonly -race -count=1 -timeout 5m \
  -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' \
  ./internal/privacy ./internal/nlog ./cmd/yz-agent

go test -mod=readonly -race -count=1 -timeout 5m \
  -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' \
  -run 'RuntimeLog|RunningLogFactory|PanelLogLevel|CustomConfig.*Privacy|Privacy.*Log|Log.*Privacy' \
  ./internal/kernel/xray ./internal/kernel/singbox ./internal/config
```

面板使用 PHP 8.4.27、PHPUnit 11.5.57 和 SQLite 内存数据库，相关 58 项、434 条断言通过；记录等级共用常量后，策略与归属 14 项、152 条断言再次通过。验证机器与旧节点握手等级、隐私策略、运行日志入库、等级筛选、保存开关、批次防重、节点归属和 Xray 崩溃通知。

```sh
php vendor/bin/phpunit --do-not-cache-result \
  --filter 'AgentLogPolicyTest|AgentPrivacyTest|AgentLogOwnershipTest|LogSettingsTest|LogManagementTest|TelegramXrayCrashTest'
```

面板没有修改日志 SQL、事务或迁移，未运行 MariaDB 完整发布矩阵，也未验证工作区并行进行的直出池和管理端改动。本次来源为两个仓库当前未提交工作区；Agent 基线 `78e3af7`、面板基线 `f750121`，后续提交、发布及生产更新另按项目流程执行。

日志、文件保护、采集、面板客户端、配置、sing-box 日志适配及两个命令入口的静态检查通过。扩展到 Xray 包的完整 `go vet` 发现原有 `internal/kernel/xray/dispatcher.go:267`、`:276` 上下文取消路径告警；该文件与工作区基线一致，本次未修改，不能把扩展静态检查表述为全部通过。

Linux amd64、arm64 的完整 Agent 构建通过，使用正式功能标签、只读模块依赖和禁用 CGO。程序版本为 v2.40.0，构建来源为 `78e3af7d992f241bdb8a3f77716c89c80de483b3` 加当前修改，元数据明确标记 `vcs.modified=true`；实际模块仍为 YZ-sing-box v1.16.0、YZ-Xray-core 固定提交 `36f4516090e3`（核心版本 v26.12.0）。原始基准样本、测试摘要和构建来源见[机器可读验证记录](log-performance-verification.json)。临时编译产物和中间日志在任务结束后清理。
