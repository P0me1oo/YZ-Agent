# YZ-Agent v2.38.0 发布

维护者：P0me1oo。日期：2026-11-16。

[v2.38.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.38.0) 已正式发布并设为 Latest，源码固定为 `0708e9d2de376037374467cbfcb2687d4a39ef8f`。包含全部 DHT 观察和日志、用户撤权、DNS 接管计量、WireGuard 成功写入及历史累计修复。

两个核心正式来源为 YZ-sing-box v1.16.0 与 YZ-Xray-core v26.12.0，模块锁文件和程序构建标识匹配对应完整提交，没有本地核心替换。

整仓竞态回归 41 包、1018 个顶层用例通过，六个既有原生网络/端口环境用例跳过；8 包依赖竞态与 10 份安装器检查通过。最终固定核心的 DHT/撤权/DNS 双核心实连竞态专项通过；没有把跳过的原生环境验证计为通过。

Linux amd64/arm64 Agent、xbctl 及 xboard-node 兼容文件，共 12 份附件全部匿名下载并验证，版本、核心来源、架构、VCS revision、CGO=0 和 vcs.modified=false 一致；四个程序的内嵌安装器与附件逐字节一致。

日常更新使用面板“服务器管理”升级入口，以成功回执及新鲜的 v2.38.0 版本回报确认。生产更新结果另记录于面板部署文档。GitHub 自动检查因账单锁定未执行，发布依据为本机验证；没有发布 Docker 镜像。详见[核验数据](release-2.38.0-verification.json)。
