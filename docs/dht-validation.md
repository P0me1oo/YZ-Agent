# DHT 开发验证记录

维护者：P0me1oo。日期：2026-11-16。

本次为本地未提交开发验证。工作区起点：Agent `0ce56d23ef0c04282bf0d22d2d5698f16e408090`、YZ-sing-box `3c602031097cc8e5a690c3b57c7a37ee67caf6c7`、YZ-Xray-core `3c480b0c0d736157547f80036c0eb4687d3f1586`。候选版本分别为 `v2.38.0`、`v1.16.0`、`v26.12.0`，未改正式构建标识、创建提交或发布。

## 发布前固定来源

最终核心来源为 YZ-sing-box v1.16.0 / `ad0c0cfbc7c3827f3058aa666d9f1a3b0e0fd085` 与 YZ-Xray-core v26.12.0 / `36f4516090e3e2fe88dcbe0840126582cd6fb795`，Agent v2.38.0 正式模块和版本标识已锁定上述来源。额外补齐普通用户撤权/凭据轮换后旧 UDP 会话的 DHT 日志归属，修复前两项实连用例均记录为用户 0，修复后通过；整仓与专项结果见[最终核验记录](traffic-accounting-verification.json)。下文工作区起点及开发产物作为历史保留。

## 识别和拦截范围

两个核心按完整规范编码识别 BEP 5 标准查询、响应和错误，覆盖 BEP 32 IPv6、BEP 33 scrape、BEP 43 只读节点、BEP 44 get/put 与可变数据、BEP 51 infohash 采样。未使用仅按前缀、端口或文本关键词判断的方式。

结果归入原有 BT 协议。只有绑定 BT 拦截规则且实际命中原有条件才丢弃报文；保留地址、端口、网络、入站、用户、规则顺序和中转选路。后续报文使用自己的目标，普通 UDP 与格式不符的报文继续转发，不把整个会话永久标记为 BT。没有 BT 拦截规则时建立的会话不新增检查器；新增规则后按现有重载方式建立新会话。

Agent 接收核心已确认的拦截事件，不解析负载。日志复用节点、规则、用户、目标和 UDP 字段；共享身份的中转落地继续记为用户 `0`。同一逻辑会话、目标和规则只记录一次，不重复登记连接、流量或普通访问记录。

## 优化后验证（2026-11-16）

本节对应当前工作区源码；下文原有完整回归和开发产物保留为优化前历史记录。性能基准和方法见 [DHT 性能记录](dht-performance.md)。

- 修复 BEP 32 未知地址类型及 BEP 44 合法长字典键、大整数漏判；解析器改为原地扫描。
- 修复 Xray `routeOnly` 下实际目标 IP 丢失造成的漏拦和越过优先放行；首包 DHT 命中时改由逐报文检查器处理，同会话普通 UDP 可继续。
- 恢复 sing-box 单包/批量读取、嗅探缓存过滤和写入快速路径；规则候选随热更新切换，纯 TCP 条件不启用 UDP 检查器。
- uTP 新会话只依据完整 SYN，避免 WireGuard 握手误判。扩大回归发现 VLESS 响应头状态与预留空间计算存在竞态，已改为原子读写，并增加响应头不重复写入及并发读取回归。

| 检查 | 当前结果 |
| --- | --- |
| sing-box 竞态检查 | DHT、嗅探、route、route/rule、VLESS 兼容包通过；包含读取边界、规则热更新、协议头预留、批量过滤、缓存过滤和并发响应状态。 |
| Xray 竞态检查 | DHT、BT、QUIC、dispatcher、router 共 5 包通过；包括 routeOnly 实际目标 IP、单报文边界与缓存生命周期。 |
| Agent 双核心实连专项 | 通过，44.629 秒；含合法扩展、DHT 首包、同会话普通 UDP、原生 UDP、XUDP、sing-mux、交叉中转和日志归属。 |
| Agent 扩大竞态回归 | 修复 VLESS 竞态后通过。sing-box 组合包 171.530 秒，Xray 包 1.061 秒；覆盖 DHT、路由、拦截、UDP、多路复用。 |
| 模糊测试 | 两核心各 30 秒，共 6,007,253 次（3,023,625 + 2,983,628）；无崩溃、输入修改或尾随数据误识别。 |
| 正式固定依赖兼容 | 不替换正式模块文件，两个 Agent 核心适配包编译通过。 |
| 开发构建 | Agent、sing-box、Xray 的 Linux amd64 构建通过，均为 CGO=0；检查固定起点、vcs.modified=true 和本地模块替换来源。 |

优化后两个核心的解析器 SHA256 为 `ef5c864137ea85ec37a0c81bdd8522666daca6593981c7d1a3d56cb202be5dc5`。当前修改/新增 Go 文件集合按“排序后的相对路径、空字节、文件内容、空字节”计算：

| 项目 | Go 文件数 | 当前集合 SHA256 |
| --- | ---: | --- |
| YZ-sing-box | 16 | `75a6612cd16e6e9d3711cd7968856d963512c08e97575ec9b61dfe2aa22ba8ab` |
| YZ-Xray-core | 11 | `6aacc5785fd2d618cbc56deeee9cd281f09fe04f8cce5683b68794c5efeb3bc3` |
| YZ-Agent | 4 | `0731c2be5eb19cd5cfd184fb83460f50aeed253c329485fe9c79597c372614ad` |

当前开发构建没有更改正式版本常量；程序源码由上述文件集合和模块来源标识。产物仅用于本地验证，不作为发布附件：

| 程序 | SHA256 |
| --- | --- |
| yz-agent | `d4d2ada15aa54ad2f7f2889d1669f0f560e4d0cd66060cc8284e09b60f7645f2` |
| sing-box | `43c3853fc293a4b44ec83811360b169188d4b006547d59588066c384f926e6bd` |
| xray | `8ef20e0345d0bde5a25f13153fe1d06662605bf8791de4ae961daafe6e8affa3` |

新增专项命令（竞态环境继续使用临时 Zig 0.15.2 和 CGO=1）：

```bash
# YZ-sing-box
go test -mod=readonly -race -count=1 -timeout=3m \
  -tags 'with_quic with_utls with_wireguard with_acme with_clash_api' \
  ./common/dht ./common/sniff ./route ./route/rule ./third_party/sing-vmess/vless

# YZ-Agent：临时模块文件按下文方式指向两个本地核心。
go test -modfile="$task_dir/agent.mod" -mod=readonly -race -count=1 -timeout=10m \
  -tags 'integration with_quic with_utls with_wireguard with_acme with_clash_api' \
  -run 'Test(DHT|.*Route|.*Block|.*UDP|.*Multiplex)' \
  ./internal/kernel/singbox ./internal/kernel/xray
```

Xray 路由测试使用同一份 geosite 数据（下文有校验值）；只通过临时 Go 覆盖文件更改数据路径，未修改或跳过原断言。本次未提交、推送、发布或部署。临时编译器、覆盖文件、测试数据及程序在验证完成后清理。

## 优化前验证结果（历史记录）

环境：Linux amd64、Go 1.27.3。竞态检查显式启用 CGO，使用任务临时目录中的 Zig 0.15.2 编译器，复用已有编译缓存，没有安装系统软件或修改全局 Go 配置。普通构建使用 `CGO_ENABLED=0`。

| 检查 | 结果与覆盖 |
| --- | --- |
| sing-box 竞态回归 | `common/dht`、`common/sniff`、`route`、`route/rule` 全部通过；包含严格编码、读取边界、后续报文、目标及协议头预留、三层 UDP 转入检查路径、日志去重、读错误与已带检查器会话的规则撤销。 |
| Xray 竞态回归 | `common/dht`、BT、QUIC 协议、dispatcher、router 全部通过；包含 DHT 单包输入、QUIC 汇集负载保留、普通 UDP 片段不能拼成 DHT、混合批次顺序、每份目标、共享会话不被修改、中断及日志去重。 |
| DHT 模糊测试 | 两核心各 30 秒，分别执行 3,048,895 和 3,018,148 次，共 6,067,043 次，无崩溃、输入修改或尾随数据误识别。 |
| Agent DHT 实连 | 双核心 Shadowsocks UDP 与 VLESS XUDP、同会话后续报文、目标端口和地址条件、TCP 条件排除、无绑定、优先放行、普通 UDP/截断/伪造 KRPC 对照均通过。 |
| 交叉中转 | sing-box/Xray 入口与落地四种组合，分别验证入口规则只约束入口自身线路、落地规则作用于中转流量和落地用户记为 `0`，全部通过。 |
| 多路复用与重载 | sing-mux 同一物理连接的两个 UDP 子会话独立检查、普通报文继续通过；两核心重复重载与规则编号变化后的新会话日志归属通过竞态检查。 |
| Agent 扩大竞态回归 | 两核心、访问日志、模型、面板通信、控制面及服务层共 7 包全部通过，核心组合实连包耗时 749.886 秒。最终 UDP 嗅探入口的兼容补充由核心 BT/QUIC/dispatcher/router 与 Agent 路由、DHT、拦截日志专项复核通过；后者耗时 122.077 秒，其余结果按不受影响范围复用。 |
| 正式固定依赖兼容 | 使用未修改的正式模块文件，两个核心适配包编译与固定构建标识测试通过；可选日志观察接口不要求旧核心新增类型或函数。 |
| 开发构建 | 三个程序的 Linux amd64 构建通过，核对 `GOOS=linux`、`GOARCH=amd64`、`CGO_ENABLED=0`、各仓库完整起点及 `vcs.modified=true`。Agent 实际模块替换路径为两个本地核心，未把旧固定依赖当作本次联调来源。 |

Xray 原路由测试要求 `resources/geosite.dat`，工作区未带该文件。验证从 XTLS/Xray-rules-dat 的官方发布下载数据到任务临时目录，仅通过 Go 测试覆盖文件调整该测试的数据路径；原断言完整保留，未跳过用例或把测试数据写入源码目录。

## 优化前来源和构建校验（历史记录）

两个核心的 `common/dht/message.go` 内容相同，SHA256 为 `318536f0acf349cec27dfce9f7a5e51b2a3d11db8aa36bbbba630d8742002ff4`。本次最终 Go 文件集合的校验以排序后的相对路径、空字节、文件内容、空字节依次串联计算；包含相对于上述起点修改和新增的 Go 文件，不包含纯文档。

| 项目 | Go 文件数 | 最终 Go 文件集合 SHA256 |
| --- | --- | --- |
| YZ-sing-box | 8 | `fe55eb49b4d11b593ae0a4f909d770e0ac316cf2ed546e557728b20cbb654230` |
| YZ-Xray-core | 11 | `a32797c5df6fb8690eba145c77775de9dfebb5eac04268addc918e5a96d3cc51` |
| YZ-Agent | 4 | `893286b9f15a77beefdca6573737efc86aeab4446a69326e09b42dbfad4b404a` |

最后一次开发构建实际包含最终源码，三个 ELF 程序均为 Linux amd64、静态链接。Agent 使用 `v2.38.0-dev` 临时构建标识，两个核心的正式版本常量未改，实际源码以模块路径、完整起点、未提交标志和上述内容校验区分。

| 开发程序 | SHA256 |
| --- | --- |
| yz-agent | `1c5a230faff255397352789c1e67052547fdbdc318549ea3be1a8ff063ee9d5f` |
| sing-box | `b5fb66d52d46fb7c022f22d0d7f9d58c5e0c42e4ddffbc08ba6b59a1d81c761a` |
| xray | `e5431eae0d623ed32eec28a5e2f0fd55ac827726e4b0836471e39aefb2bee4b0` |

路由测试使用的 geosite 数据 SHA256 为 `59e45b97236f4aa028a88b12c14eb01f14e25baed38ef47eee318eb2492204b9`。开发程序、测试数据、临时模块文件和编译器仅用于本次验证，结束后清理，不作为发布附件或运行实例保留。

## 复现方式

在各核心项目根目录执行受影响包的竞态检查：

```bash
go test -mod=readonly -race -count=1 -timeout=3m \
  -tags 'with_quic with_utls with_wireguard with_acme with_clash_api' \
  ./common/dht ./common/sniff ./route ./route/rule

go test -mod=readonly -race -count=1 -timeout=3m \
  ./common/dht ./common/protocol/bittorrent ./common/protocol/quic \
  ./app/dispatcher ./app/router

go test -mod=readonly ./common/dht -run '^$' \
  -fuzz '^FuzzIsMessage$' -fuzztime=30s -parallel=2
```

Xray 的第二条命令需要其原路由测试所要求的 geosite 数据。本次仅覆盖测试数据路径；该覆盖文件和数据属于临时验证资料。

在 Agent 项目根目录创建临时模块文件，正式 `go.mod` 和 `go.sum` 不修改：

```bash
task_dir=$(mktemp -d /tmp/yz-dht-XXXXXX)
cp go.mod "$task_dir/agent.mod"
cp go.sum "$task_dir/agent.sum"
go mod edit -modfile="$task_dir/agent.mod" \
  -replace=github.com/sagernet/sing-box=/root/YZboard/YZ-sing-box \
  -replace=github.com/xtls/xray-core=/root/YZboard/YZ-Xray-core
go test -modfile="$task_dir/agent.mod" -mod=readonly -race -count=1 -timeout=15m \
  -tags 'integration with_quic with_utls with_wireguard with_acme with_clash_api' \
  ./internal/kernel/singbox ./internal/kernel/xray ./internal/accesslog \
  ./internal/model ./internal/panel ./internal/controlplane ./internal/service
```

竞态命令需可用的 C 编译器及 `CGO_ENABLED=1`。测试只创建回环临时监听、内存测试身份和临时配置，不修改产品监听端口或生产服务。

## 交付状态

三个项目源码、测试、兼容说明和独立版本日志保留为未提交修改。正式固定依赖仍为 sing-box `v1.15.1` 和 Xray `v0.0.0-20261103192507-3c480b0c0d73`；按这些旧依赖构建不会自动包含新 DHT 功能。需要新核心固定源码可获取后，才更新 Agent 固定依赖与构建标识并正式发布。本次没有提交、推送、Tag、Release、镜像发布或部署，也未修改面板识别逻辑。
