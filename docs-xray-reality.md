# Xray REALITY 最低客户端版本

## 背景

xray-core 从 v26.7.11 起，如果生成的 `realitySettings` 中没有 `minClientVer`，内核会自动写入内置下限 `26.3.27`。低于该版本的客户端在 REALITY 握手阶段会被服务端拒绝，表现为节点可用但部分旧客户端连不上。

YZ-Agent 生成 Xray 配置时总是显式写入 `minClientVer`，默认值为 `0.0.0`，即不限制客户端版本。需要主动拦截旧客户端时，再按下面的方式抬高门槛。

## 配置项

| 配置项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `kernel.reality_min_client_ver` | 字符串 | `0.0.0` | 写入 Xray REALITY 入站的 `realitySettings.minClientVer` |

生效条件：`kernel.type` 为 `xray`，且节点在面板中启用 REALITY（`tls = 2`）。sing-box 的 REALITY 没有对应字段，该配置对 sing-box 内核无效。

## 取值规则

- 必须是三段十进制数字，形如 `x.y.z`，每段取值范围 `0-255`，例如 `0.0.0`、`1.8.4`、`26.3.27`、`255.255.255`。
- 留空、缺省或整个 `kernel` 段未写，都按默认值 `0.0.0` 处理。
- 格式非法（段数不是三段、含非数字字符、单段超过 255）时，进程在启动阶段直接报错退出，不会带着错误配置启动内核。

## 配置示例

单实例配置：

```yaml
panel:
  url: "https://panel.example.com"
  token: "your-api-key"
  node_id: 1
kernel:
  type: "xray"
  reality_min_client_ver: "0.0.0"
```

多实例配置中，顶层 `kernel` 的取值会被各实例继承，实例内显式填写的值优先：

```yaml
kernel:
  type: "xray"
  reality_min_client_ver: "0.0.0"   # 所有实例的默认值
instances:
  - panel:
      url: "https://panel.example.com"
      token: "api-key-1"
      node_id: 1
  - panel:
      url: "https://panel.example.com"
      token: "api-key-2"
      node_id: 2
    kernel:
      reality_min_client_ver: "26.3.27"   # 仅该实例要求较新客户端
```

## 修改与验证

配置文件修改后重启节点服务，内核会按新值重新生成 REALITY 入站：

```bash
yz-agent service restart
```

Xray 配置由 Node 在内存中生成并直接交给内核，磁盘上没有可查看的 `config.json`。是否生效以实际握手结果为准：

- 填写 `0.0.0` 时，旧版本客户端应能正常完成 REALITY 握手；
- 抬高到某个版本后，低于该版本的客户端握手会被服务端拒绝，客户端侧通常表现为连接被重置或超时；
- 填写非法值时，节点服务启动失败，日志中会给出 `kernel.reality_min_client_ver` 的具体错误原因。

## 注意事项

- 该配置只影响服务端接受的客户端版本下限，不改变 REALITY 的密钥、`serverNames`、`dest` 等参数，这些仍由面板下发。
- 抬高下限属于兼容性收紧操作，会直接影响仍在使用旧客户端的用户，调整前先确认用户端版本分布。
- `yz-agent bind` 等会重写配置文件的命令保留该字段，不会在改绑节点时丢失已有取值。
