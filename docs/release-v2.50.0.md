# YZ-Agent v2.50.0 发布

日期：2026-11-25。维护者：P0me1oo。

[v2.50.0](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.50.0) 已发布并设为 Latest，固定源码 `d9a48fb048e7d854fa79da599b89e01e2a2a8a26`。配套 YZboard 2.29.0 / Dash 0.111.0，两个核心使用面板确认的落地 IP，地址变化触发连接配置更新。保留域名、TLS 身份、端口和凭据；旧面板继续使用原地址。说明见[落地 DDNS](landing-ddns.md)。

来源为 `release/v2.50.0`，基于 v2.48.0 的 `300453fe3dceeb0253f61202ef979e60159c06bc`。原 `dev` 工作区的 v2.49.0 Next 开发改动未提交、未包含、未覆盖；后续在该任务整合本发布提交。没有把独立发布分支声明为已合入 dev。

[固定来源 CI](https://github.com/P0me1oo/YZ-Agent/actions/runs/45644299125) 全部通过：完整竞态 1,154 个顶层用例通过、9 项环境用例按条件跳过；固定依赖 43 项、Mihomo 客户端 2 项与安装检查通过；四组原生防火墙检查通过；Linux amd64/arm64 构建及源码信息检查通过。新增 DDNS 真实地址切换和三种协议配置检查均在竞态检测下通过。见[CI](release-v2.50.0-ci.json)、[统计](release-v2.50.0-tests.json)。

13 份附件已匿名下载，大小和 SHA-256 与 GitHub 摘要一致；工作流生成的二进制、安装器、构建信息与 SHA256SUMS 一致，补充 release.json 单独按 GitHub 摘要核验。两个架构的 Agent 及兼容文件名逐字节相同，内嵌源码匹配固定提交、CGO_ENABLED=0、vcs.modified=false。核心依赖保持 YZ-sing-box v1.16.0 与 YZ-Xray-core 固定提交 36f4516090e3。见[附件核验](release-v2.50.0-verification.json)。

未升级运行节点，未发布 Docker 镜像。通过面板“服务器管理”升级全部前置并确认版本后启用落地 DDNS；IP 改变会重建连接配置，原连接可能短暂断开。回退 v2.48.0 前先关闭功能并核对落地手填 IP。
