# go-llm-gateway

Go 模型网关学习与工程项目，当前为初始化骨架，功能待实现。

## 运行

需要 Go 1.26 或兼容的新版本。

```sh
go run ./cmd/gateway
go build ./...
```

## 开发顺序

1. Go 数据结构、JSON 校验与最小 HTTP 服务。
2. 模拟模型上游与普通请求转发。
3. MySQL 账本、逻辑调用标识与并发幂等。
4. 预算预留/结算、UNKNOWN 状态、取消和故障测试。
5. SSE、Redis 限流、性能分析和对照实验。

尚未接入模型、MySQL 或 Redis，暂无性能或竞争优势结论。
API Key 只通过本地环境变量配置，不提交到 Git。

配套项目：[go-agent-runtime](https://github.com/Yukinoshita03/go-agent-runtime)。
