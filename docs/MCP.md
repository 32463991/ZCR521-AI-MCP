# MCP 与 HTTP 接口

## 版本协商

`/mcp` 使用固定的官方 Go SDK `v1.7.0-pre.3`：

- `2026-07-28`：先调用 `server/discover`，Stateless 模式，不创建 MCP 会话，不提供独立 GET 流。
- `2025-11-25` 及兼容旧版本：使用 `initialize`、`notifications/initialized`、`ping` 与 `Mcp-Session-Id`。
- `2024-11-05`：使用 `GET /sse` 建立 SSE，再向事件中给出的 `/messages?sessionid=...` POST。

协议错误使用 JSON-RPC error。工具自身执行失败返回 `isError: true`，同时在 `structuredContent` 中返回统一错误结果，不会把失败包装成成功。

## 工具结果

每个工具固定返回：

```json
{
  "success": true,
  "code": "OK",
  "message": "完成",
  "data": {},
  "error": null,
  "stdout": "",
  "stderr": "",
  "exitCode": 0,
  "durationMs": 12,
  "taskId": "",
  "rebootRequired": false,
  "artifacts": [],
  "strategy": "native"
}
```

Schema 使用 JSON Schema 2020-12；每个工具都要求 `action`，用 `enum` 与 `oneOf` 描述允许动作，并标记只读、幂等、破坏性和重任务属性。

## 持久任务

工具参数设置 `"background": true` 会先把任务及其日志位置持久化，再开始执行。任务与日志以原子 JSON/JSONL 保存到内部状态目录，不依赖数据库，客户端断开不会取消后台任务。

在 `2026-07-28` 请求的逐请求 `_meta.io.modelcontextprotocol/clientCapabilities.extensions` 中声明 `io.modelcontextprotocol/tasks` 时，`tools/call` 返回标准 `CreateTaskResult`（`resultType: "task"`）；未声明扩展的客户端得到普通 `CallToolResult`，其中保留 `TASK_ACCEPTED`、`taskId`，并可通过 `zcr521_task` 查询。

声明扩展的客户端可调用：

- `tasks/get`
- `tasks/update`
- `tasks/cancel`

每个任务请求还必须发送 `Mcp-Method`，以及等于工具名或 `taskId` 的 `Mcp-Name`。未逐请求声明扩展返回 `-32021` 与 HTTP 400；未知任务 ID 返回 `-32602`。当前扩展已经移除 `tasks/list` 和 `tasks/result`，调用它们返回 `-32601`。

`tasks/get` 返回 `resultType: "complete"` 和标准状态 `working`、`completed`、`failed` 或 `cancelled`。Android 工具执行错误属于完成的 `CallToolResult`，因此表现为 `completed` 且内嵌 `isError: true`，不会伪造 JSON-RPC 失败。`tasks/update` 接受标准 `inputResponses`；为兼容首版 ZCR521 客户端，也接受 `progress` 与 `message`，但客户端不能伪造终态。

普通轮询使用 `Accept: application/json`，立即返回当前状态。显式只发送 `Accept: text/event-stream` 时，服务器在同一 POST 流上发送 `notifications/tasks`，并额外发送首版约定的 `notifications/tasks/status` 兼容别名。携带两种 Accept 类型的标准客户端仍使用即时 JSON 轮询语义。

## 上传

1. 通过 `zcr521_transfer_upload` 的 `create` 动作取得随机 ID；HTTP 兼容入口也可创建。
2. 向 `/transfer/upload/{id}` 执行 `PUT`，每块最多 4 MiB。
3. 每块必须携带 `Content-Range: bytes <start>-<end>/<total>`。
4. 服务器检查连续偏移、总大小和可选 SHA-256，重复提交同一已写区间不会静默覆盖。
5. 查询状态后从已接收偏移继续。

传输总大小默认只受剩余磁盘和可选配置限制。元数据与内容均流式处理，不把完整文件放进 JSON。

## 下载与 ResourceLink

大文件结果返回 `ResourceLink`，URI 为随机 UUID 的 `/transfer/download/{id}`，默认一小时过期。下载支持标准 `Range` 与流式响应；路径本身不会暴露给网络客户端。无法直传 HTTP 的客户端可使用 MCP 4 MiB 分块动作。

## STDIO 桥

```sh
zcr521-bridge --url http://127.0.0.1:5322/mcp
```

桥从 stdin 读取逐行 JSON-RPC，并转发到 Streamable HTTP；对旧式 SSE 客户端可选择兼容模式。桥把协议输出写 stdout，把诊断写 stderr，避免污染 JSON-RPC 通道。

## 安全请求规则

- 只允许回环或当前直接连接网段的源地址。
- 校验 `Host`，防止 DNS rebinding。
- 浏览器携带 `Origin` 时必须是同源或显式白名单。
- 不返回 CORS 许可头；OPTIONS 被拒绝。
- 限制 Header、普通请求体、连接数和任务并发。
- 蜂窝/公网来源以及经路由到达的非 on-link 地址被拒绝。
