# go-llm-gateway

Go 模型网关学习与工程项目，已支持模拟聊天、DeepSeek 非流式文本适配、DeepSeek SSE 流式转发、超时取消、基础读取限制及 JSON 错误。

## DeepSeek 非流式聊天

启动网关前在本地终端设置 `GATEWAY_PROVIDER=deepseek` 和 `DEEPSEEK_API_KEY`，或在 GoLand 的运行配置中填写这两个环境变量。密钥不要写入源码或提交到 Git。

```sh
export GATEWAY_PROVIDER=deepseek
# 在本地设置 DEEPSEEK_API_KEY 后运行：
go run ./cmd/gateway
```

另一个终端：

```sh
curl -sS --max-time 70 http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"用一句话解释 Go 接口"}]}'
```

官方接口：`https://api.deepseek.com/chat/completions`；Bearer 鉴权；网关上游超时60秒，响应读取上限1MiB。模型名由聊天请求提供，以账户可用模型为准。DeepSeek模式请使用聊天路由，旧`/echo`仍发送mock-model，仅用于模拟模式。

保留上游返回的 model、choices、finish_reason、id/object/created及usage，不虚构用量。当前支持文本非流式和SSE流式子集；工具调用、图片、推理内容和其他参数的完整兼容尚未实现；上游非200统一映射502。真实账户调用已完成基本联调，但不等于完整协议兼容已经验收。

## DeepSeek SSE 流式聊天

请求体中的 `stream` 设置为 `true` 后，网关会把上游的 `text/event-stream` 正文逐段写给客户端，并在每次写入后刷新输出缓冲。流式响应已经开始后，如果上游中途断开，网关不能再改发普通的 502/504 JSON，只能结束当前流；客户端应检查是否正常收到结束事件。

```sh
curl -N -sS --max-time 70 http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"用三段话解释 Go 的 interface"}]}'
```

流式代码路径使用本地模拟上游测试了 `stream:true`、鉴权、`text/event-stream`、分段到达和网关逐段转发；用户已在本地用真实 DeepSeek 账户验证流式输出。网关按 SSE 空行识别事件，校验 `data:` JSON 和 `[DONE]`，随后原样转发事件；当前提取的 `delta.content`用于校验，尚未改造成聚合后的普通 JSON。`GATEWAY_PROVIDER=mock` 不支持 `/v1/chat/completions` 的 `stream:true`，会返回 501；`/stream-demo` 仍可独立演示 Flush。

不设置GATEWAY_PROVIDER（或设为mock）时使用本地8081模拟服务，另运行 `go run ./cmd/provider`。
验证：`go test -race ./...` 和 `go vet ./...`。
参考：[DeepSeek官方入门](https://api-docs.deepseek.com/)、[聊天API](https://api-docs.deepseek.com/api/create-chat-completion/)。

## 运行

需要 Go 1.26 或兼容的新版本。

```sh
go run ./cmd/gateway
go build ./...
```

## 开发顺序

1. Go 数据结构、JSON 校验与最小 HTTP 服务。
2. 模拟模型上游与普通请求转发。
3. DeepSeek真实非流式联调与SSE流式转发。
4. API Key鉴权、并发上限、有限重试及请求追踪。
5. 性能分析和基准报告；复杂预算账本与UNKNOWN恢复为进阶选修。

DeepSeek非流式和SSE真实调用均已由用户本地终端验证。尚未接入MySQL或Redis，暂无性能或竞争优势结论。
API Key 只通过本地环境变量配置，不提交到 Git。

配套项目：[go-agent-runtime](https://github.com/Yukinoshita03/go-agent-runtime)。
