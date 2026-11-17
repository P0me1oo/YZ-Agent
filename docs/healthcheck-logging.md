# 健康检查日志修复

## v2.40.1 网络空间监听归属修复

发布完整检查确认：WireGuard 创建隔离网络空间的锁定线程可能是进程主线程，线程退出后 `/proc/<进程编号>/net/tcp` 不再代表运行线程的网络空间。此前仅使用进程编号读取时，真实 Reality 监听会被误报为缺失；独立场景通过而前序 WireGuard 场景后失败。

Linux 读取期间固定当前线程，用线程编号访问该线程的连接表及同一线程组共用的描述符，再将归属编号规范为实际进程编号。只核对该进程拥有的监听，不建立空连接；外部 HOST_PROC 设置、读取失败和取消仍按原保护处理。其他平台保留原读取方式。

新增独立网络空间回归：主线程维持原网络空间，另一个锁定线程在临时空间内创建真实 TCP 监听。旧实现稳定返回缺失；修复后正确识别，并保留进程归属。临时网络空间在检查后恢复和回收。最终完整结果与固定来源在正式发布记录中补交；以下保留 v2.40.0 实现阶段的验证。


维护者：P0me1oo。日期：2026-11-17。候选 Agent v2.40.0，本地未发布、未部署。

## 问题与改动

本机监听检查原先建立 TCP 连接后立即关闭；连接成功却没有完成 Reality 等协议的握手，核心因此产生运行错误。中转检查的 DNS 首选地址族来自入口，入口支持 IPv6 不代表落地支持；每轮从相同的失败地址开始，会在最终回退成功的同时重复制造落地错误。

本次修复在 Agent 完成，两个核心和锁定依赖不变：

1. 监听检查读取当前进程拥有的 TCP LISTEN 记录，核对监听地址和端口，不发送空连接。其他进程占用该端口不能作为本节点监听成功的依据。Linux 使用实际 `/proc`，外部主机监控的读取路径不改变进程归属；读取失败显示未检查，缺少监听显示失败。该项延迟为状态读取耗时，不代表入口连接延迟。
2. 首次健康目标地址排序优先 IPv4，保留去重后的全部已校验 IPv4、IPv6 地址并交替回退。只有 IPv6 的目标仍可以检查。
3. 每份核心配置和检查目标持有独立的地址记录，各出站只记住最近完成 HTTPS 且返回 2xx 的地址。每轮重新解析、核对全部公网答案；优先地址必须仍在最新答案中，失败后继续尝试其余答案。不同落地不共用成功地址，配置或目标改变、关闭检查及核心停止时丢弃记录，进程重启后重新确认。

请求仍验证目标域名证书，不跟随重定向，HTTP 非 2xx 响应直接判为失败。单条出站共享八秒、整轮三十秒和最多四路并发的预算保持；地址尝试没有增加并发数，延迟包含实际回退耗时。

没有全局过滤日志，没有改变日志开关、最低上传等级、隐私加密或通知规则。首次遇到不可用地址、链路变化或所有地址失败时，真实失败仍可能记录；这一部分保留诊断价值。既有中转协议没有探测身份标记，不能可靠地仅靠入口上下文区分落地端的全部探测日志。

## 验证

修改前两项定向检查失败，确认旧排序先尝试入口 DNS 首选 IPv6，旧监听检查确实接受到空连接。修复后使用 Go 1.27.3、Linux amd64、Zig 0.15.2、正式功能标签和锁定依赖验证：

- 健康检查完整并发回归 23 项通过；服务层 117 项通过，2 项既有原生来源 SOCKS 用例因缺少专用隔离网络环境跳过。
- 双核心、运行日志与配置相关 15 项并发检查通过，无数据竞争。实际 Reality 入站分别覆盖 sing-box、Xray 和普通／隐私模式；连续监听检查不产生核心错误，停止核心后不再判为监听正常。sing-box 的真实无效连接仍按 error 记录。
- 地址复用覆盖出站隔离、重复检查、优先地址失效、DNS 移除地址、TLS 失败回退、HTTP 错误停止、IPv6 单栈、并发、原始 DNS 切片不被修改、八秒预算和取消。配置或目标改变、停用和核心停止的清理路径通过。
- 监听检查覆盖进程归属、IPv4／IPv6、通配绑定、IPv4 映射地址、端口不匹配、已建立连接不冒充监听、读取失败、取消、外部 HOST_PROC 设置及实际监听关闭。
- 健康检查、服务层及 sing-box 相关静态检查通过。没有修改 Xray 的既有业务代码，未扩大为整仓静态或全产品发布检查。

验证命令：

```sh
go test -mod=readonly -race -p 2 -count=1 -timeout 5m \
  -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' \
  ./internal/healthcheck ./internal/service

go test -mod=readonly -race -p 2 -count=1 -timeout 3m \
  -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' \
  -run '^(TestHealthListenerInspection|TestOperationsProbeBothCores|TestRuntimeLog|TestRunningLogFactory|TestPanelLogLevel|TestCustomConfig.*Privacy|TestPrivacy.*Log)' \
  ./internal/kernel/singbox ./internal/kernel/xray ./internal/config

go vet -mod=readonly \
  -tags 'with_quic with_utls with_wireguard with_gvisor with_acme with_clash_api' \
  ./internal/healthcheck ./internal/service ./internal/kernel/singbox
```

本机复用已有 SDK 和编译工具，没有安装系统软件。测试使用独立临时身份、数据和回环监听，没有在生产节点进行故障注入或修改运行状态。

## 构建与交付状态

Linux amd64、arm64 的完整 Agent 构建通过，候选版本 v2.40.0，正式功能标签、`CGO_ENABLED=0`、只读模块依赖。来源为 `78e3af7d992f241bdb8a3f77716c89c80de483b3` 加当前工作区修改，构建元数据明确标记 `vcs.modified=true`。

两个架构均核对到 YZ-sing-box v1.16.0、YZ-Xray-core 固定提交 `36f4516090e3`（核心版本 v26.12.0）。验证程序 SHA-256：

| 架构 | SHA-256 |
| --- | --- |
| Linux amd64 | `ccfcaa578707c05df897d2b8c036e977d4a51b29094313681910cba363cd25cb` |
| Linux arm64 | `47619600d9dd79ca47427ea2b12bc2775a2dcd624a7228fdb73d09caa11fc12d` |

这些是包含工作区原有日志优化等修改的验证构建，不是正式安装包；没有提交、推送、发布或升级服务器。临时编译程序和测试日志在交付前清理，正式发布另按项目流程执行。
