# YZ-Agent v2.51.2 发布核验

2026-11-28 已发布，未部署到运行节点，未发布 Docker 镜像。

- 发布标签：`v2.51.2`，GitHub `latest` 已指向此版本。
- 固定源码：`4871ac1a1e025beb942ddfdef09a11b61b57ee5f`。
- 基础版本：`v2.51.1`，仅纳入 HTTP 安装人工确认、对应文件密钥领取授权和配套测试、文档。
- 发布分支：`release/http-confirm-v2.51.2`。
- [Release](https://github.com/P0me1oo/YZ-Agent/releases/tag/v2.51.2)；[CI 46127573900](https://github.com/P0me1oo/YZ-Agent/actions/runs/46127573900)。
- 发布时刻：`2026-11-28T09:07:28Z`（UTC）。

## 验证范围

发布来源的安装人工确认测试、凭据兼容测试，以及文件保护、配置、管理命令、面板客户端和 Agent 启动入口五个 Go 包的构建标签及 `-race` 检查通过。GitHub 完整测试、Mihomo WireGuard 客户端、安装器语法和 UFW/firewalld × nftables/iptables 四组原生防火墙验证全部通过。

Linux amd64、arm64 构建均通过来源核验；程序记录固定提交且 `vcs.modified=false`、`CGO_ENABLED=0`。两个架构中旧名称安装包与对应 yz-agent 内容一致。amd64 程序实际运行输出 `v2.51.2` 和固定提交，两个管理入口均确认支持 HTTP 授权保存。

12 份正式附件已通过不带 GitHub 认证的下载验证，大小与 Release 一致；校验清单与逐文件 SHA-256 一致。安装脚本逐字节等于标签内源码。sing-box 仍为 `P0me1oo/YZ-sing-box v1.16.0`，Xray 仍为 `P0me1oo/YZ-Xray-core v0.0.0-20261116081756-36f4516090e3`，未引用工作区核心。

## 使用边界

新装使用本版本安装脚本与程序。HTTP 入口必须在真实终端手动确认；回车、拒绝、输入结束或无终端时停止，`--yes` 不跳过。确认按实例绑定入口，文件加密密钥请求复用该确认；HTTPS 证书校验与密钥接口跳转限制保持启用。

已安装机器通过面板“服务器管理 → 升级”更新，并核对操作结果和节点回报版本。回滚来源为 `v2.51.1`；该旧版没有 HTTP 确认能力，文件密钥领取恢复旧版 HTTPS 限制。

## 附件 SHA-256

| 附件 | SHA-256 |
| --- | --- |
| SHA256SUMS | `e427c5ff713f2fa7c07b7690e6e5b87644146b94cbe1f872c99236926ad1123e` |
| install.sh | `f2b218f977a56695df20be27c71ef065ad513b2257f7c8c35abaa1c8650a69df` |
| xbctl-linux-amd64 | `d7ee6f94416e88877bd02aa01ce507536cbcb3466efa8de79010e68d846ab494` |
| xbctl-linux-amd64.buildinfo.txt | `1f0ff84acb92e08c4cabdda8127d57427818425a635f1b7532c422db94400db7` |
| xbctl-linux-arm64 | `e4cdc04c0d7d51f502236ecbe70d178d35044b167b7c0394281c29acd403a343` |
| xbctl-linux-arm64.buildinfo.txt | `eeb9e9e92f85b85b1772f94777777ae4f189982152c2f8531c6e46767e7715b6` |
| xboard-node-linux-amd64 | `0edffe2e58d03567204ee9e19f8833cc9f413fd951b3e88ec48809891b9c8842` |
| xboard-node-linux-arm64 | `09f4c4da2e22031c89b71f67a0e14faafe1187fa77a570b1ee19fb55ef38da6c` |
| yz-agent-linux-amd64 | `0edffe2e58d03567204ee9e19f8833cc9f413fd951b3e88ec48809891b9c8842` |
| yz-agent-linux-amd64.buildinfo.txt | `61af85c30e3d7b5b6dfb1fe7fc343f80760eba2636140289761a34684de40c5f` |
| yz-agent-linux-arm64 | `09f4c4da2e22031c89b71f67a0e14faafe1187fa77a570b1ee19fb55ef38da6c` |
| yz-agent-linux-arm64.buildinfo.txt | `71a587f7f51ed9f00b2ac823492e2cb8ed185b8c2fa9b37067ab8382e20acd2a` |
