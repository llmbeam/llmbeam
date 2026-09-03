# LLMBeam 局域网 Connector 实施计划

## 目标

让 PC A 上的本地模型服务可以通过局域网被 PC B 使用，并在 PC B 上提供一个仅监听 `127.0.0.1` 的 OpenAI-compatible endpoint：

```text
PC A: Ollama / vLLM / llama.cpp / other backends
  -> LLMBeam LAN gateway
  -> mDNS discovery + one-time pairing
PC B: llmbeam connect
  -> 127.0.0.1:<port>/v1
  -> Codex / Cursor / Continue / OpenAI SDK
```

局域网模式不依赖 GitHub Pages、Tailcat、DERP、域名或公网 rendezvous 服务。现有浏览器二维码配对和 `--remote` 互联网模式必须保持兼容。

## 产品行为

### PC A

```bash
llmbeam
```

启动后继续提供手机浏览器访问，并发布一个 mDNS 服务：

```text
service: _llmbeam._tcp.local
name: <human-readable-device-name>
port: 8442
device_id: <random-instance-id>
version: <llmbeam-version>
```

mDNS 不得包含配对码、session token、Tailcat token 或任何 API key。

### PC B

```bash
llmbeam connect
```

交互流程：

1. 发现局域网内的 LLMBeam 主机；
2. 展示设备名、IP、端口和版本；
3. 用户选择 PC A；
4. 输入 6 位 Connect Code；
5. 完成一次性配对和加密握手；
6. 在 `127.0.0.1` 启动本地 OpenAI proxy；
7. 打印本地 endpoint 和本地 API key。

示例输出：

```text
Found LLMBeam hosts:

1. Shao-Hua-Mac  192.168.50.242:8442

Select host: 1
Enter 6-character Connect Code: K7M4QX

Connected.
Local OpenAI endpoint:
  http://127.0.0.1:8333/v1
```

必须支持 mDNS 不可用时的回退：

```bash
llmbeam connect --host 192.168.50.242:8442
```

## 兼容性与安全约束

- 现有浏览器使用的 8 位 pairing code 不改动；connector 使用独立的 6 位 Connect Code。
- Connect Code 使用 `crypto/rand` 和当前无歧义 Base32 alphabet。
- Connect Code 有效期默认 5 分钟，单次使用，成功后立即失效。
- 失败尝试同时按来源 IP 和 Connect Code 限制；默认 5 次失败后锁定。
- Connect Code 只用于一次性配对，不能直接作为后续 API credential。
- 配对成功后生成独立的 256-bit connector token。
- connector 默认只监听 `127.0.0.1`；`0.0.0.0` 必须显式指定并显示安全警告。
- PC A 到 PC B 的模型请求不能长期使用明文 HTTP；生产版本使用 TLS 证书 fingerprint pinning 或 Noise/X25519 加密握手。
- PC A 不向 PC B 发送任何上游 backend API key。
- connector token 可过期、刷新和撤销；PC A 退出时全部失效。
- 不区分“code 不存在”和“code 已过期”的错误，避免枚举。

## 实施阶段

### 阶段 1：OpenAI-compatible gateway

- [x] 新增 `GET /v1/models`。
- [x] 新增 `POST /v1/chat/completions`。
- [x] 复用 `internal/backend.Registry` 和现有模型命名空间。
- [x] 将 `<backend>/<model>` 映射回上游模型名。
- [x] 保留 SSE streaming、上游错误转换和请求大小限制。
- [x] 为 `/v1/*` 增加独立的 connector authentication middleware。
- [x] 确认现有 `/api/models`、`/api/chat` 和浏览器 session 行为不变。

涉及文件：

```text
internal/server/openai.go
internal/server/openai_test.go
internal/server/server.go
```

验收：

```bash
curl http://<PC-A-IP>:8442/v1/models
```

### 阶段 2：Connector 配对模型

- [x] 在 `internal/pair` 中增加独立的 connector pairing state。
- [x] 增加 6 位 Connect Code 生成、规范化和 TTL。
- [x] 增加单次兑换、并发保护和失败限流测试。
- [x] 增加 connector session token、过期和撤销能力。
- [x] 不改变现有浏览器 8 位 code 的行为和测试。

建议接口：

```text
GET  /api/connector/info
POST /api/connector/pair
POST /api/connector/refresh
POST /api/connector/revoke
```

配对请求至少包含：

```json
{
  "code": "K7M4QX",
  "client_id": "b_<random-id>",
  "client_public_key": "<ephemeral-key>"
}
```

涉及文件：

```text
internal/pair/connector.go
internal/pair/connector_test.go
internal/server/connector.go
internal/server/connector_test.go
```

### 阶段 3：LAN discovery

- [x] 选择并引入跨平台 mDNS 库。
- [x] PC A 在 HTTP server 成功监听后发布 `_llmbeam._tcp`。
- [x] 正确处理服务名冲突、多个 LLMBeam 主机和停止广播。
- [x] PC B 实现发现、过滤、排序和用户选择。
- [x] mDNS 失败时支持 `--host` 手动连接。
- [x] 不向 mDNS 广播秘密信息。

建议抽象：

```go
type LANAdvertiser interface {
    Start(name string, port int, metadata map[string]string) error
    Close() error
}

type LANDiscoverer interface {
    Discover(ctx context.Context) ([]Peer, error)
}
```

涉及文件：

```text
internal/lan/discovery.go
internal/lan/discovery_test.go
```

### 阶段 4：`llmbeam connect` CLI

- [x] 将当前单命令 flag parsing 扩展为子命令或等价 command dispatch。
- [x] 实现 `llmbeam connect`。
- [x] 实现 `llmbeam connect --host <host:port>`。
- [x] 支持 `--listen 127.0.0.1:8333`。
- [x] 支持 `--listen 127.0.0.1:0` 并打印实际端口。
- [x] 连接失败时给出可操作错误：主机不可达、code 过期、被拒绝。
- [x] Ctrl-C 时关闭本地 listener、远端连接和临时凭证。

涉及文件：

```text
main.go
internal/connect/client.go
internal/connect/client_test.go
```

### 阶段 5：本地 OpenAI proxy

- [x] connector 只绑定 `127.0.0.1`。
- [x] 实现本地 `GET /v1/models` 转发。
- [x] 实现本地 `POST /v1/chat/completions` 转发。
- [x] 透传 SSE，不缓冲长响应。
- [x] 注入远端 connector token。
- [x] 为本地客户端生成随机 API key。
- [x] 对本地请求校验 API key。
- [x] 不转发任意路径，避免变成开放代理。
- [x] 支持上游 401、502、超时、断线和重连。

涉及文件：

```text
internal/connect/proxy.go
internal/connect/proxy_test.go
```

### 阶段 6：加密传输和设备管理

- [x] 为 PC A 生成临时 TLS 证书或实现 Noise/X25519 握手。
- [x] 配对时返回并验证 server public key/fingerprint。
- [x] 将 fingerprint 保存到 connector 配置。
- [x] 后续重连拒绝 fingerprint 不匹配的主机。
- [x] 增加 PC A 侧当前 connector 列表。
- [x] 增加 connector revoke 能力。
- [x] 明确 token 文件权限；Unix 使用 `0600`。
- [x] Windows 使用合适的用户私有存储方式。

### 阶段 7：测试、文档和发布

#### 单元测试

- [x] 6 位 code 生成、大小写和分隔符规范化。
- [x] code 过期、重复兑换和并发兑换。
- [x] IP/code 双重限流。
- [x] connector token 生成、刷新和撤销。
- [x] mDNS metadata 不包含秘密。
- [x] OpenAI 模型 ID 映射。

#### 集成测试

- [x] fake PC A gateway + fake PC B connector。
- [x] `/v1/models` 转发。
- [x] `/v1/chat/completions` 转发。
- [x] SSE 流式输出。
- [x] 上游 401、超时和断线。
- [x] PC A 重启后的失效行为。
- [x] 本地 API key 校验。
- [ ] 端口冲突和自动端口选择。

#### 跨平台验证

- [ ] macOS → macOS。
- [ ] macOS → Linux。
- [ ] Windows → macOS。
- [ ] Windows → Windows。
- [ ] macOS、Linux、Windows 防火墙提示。
- [ ] mDNS 被隔离时的 `--host` 回退。
- [ ] IPv4 环境；必要时验证 IPv6 行为。

#### 文档和发布

- [x] 更新 `README.md`。
- [x] 更新 `README.zh-CN.md`。
- [x] 增加 LAN connector 使用示例。
- [x] 说明 Codex、Cursor、Continue 的 Base URL 配置。
- [x] 说明局域网安全边界和防火墙要求。
- [x] 更新 CI、GoReleaser 和安装脚本。
- [ ] 发布包含 connector 的 minor version。

## 推荐开发顺序

```text
1. /v1/models 和 /v1/chat/completions
2. connector pairing session
3. connect --host
4. 本地 127.0.0.1 proxy
5. mDNS 自动发现
6. TLS / fingerprint pinning
7. 断线重连和设备撤销
8. 跨平台测试与发布
```

## 完成标准

在同一局域网内，用户可以完成：

```bash
# PC A
llmbeam

# PC B
llmbeam connect
```

输入 6 位 Connect Code 后，PC B 出现：

```text
http://127.0.0.1:<port>/v1
```

并且 OpenAI SDK、Codex 或其他支持自定义 Base URL 的 Agent 可以正常完成模型列表查询和流式聊天；现有手机浏览器、本地直连模式和互联网 `--remote` 模式全部通过回归测试。
