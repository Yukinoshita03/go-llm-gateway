package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Provider 描述网关需要的模型调用能力。
type Provider interface {
	Complete(ctx context.Context, req ChatRequest) (string, error)
}

// HTTPProvider 负责通过 HTTP 调用当前模拟模型。
type HTTPProvider struct {
	Client *http.Client
	URL    string
}

var _ Provider = (*HTTPProvider)(nil)

func (p *HTTPProvider) Complete(ctx context.Context, req ChatRequest) (string, error) {
	// 1. 将 req 编码成 JSON
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode upstream request: %w", err)
	}
	// 2. 用 ctx 创建上游请求
	upstreamReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.URL,
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("create upstream request: %w", err)
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	// 3. client.Do 发送请求
	resp, err := p.Client.Do(upstreamReq)
	if err != nil {
		return "", fmt.Errorf("send upstream request: %w", err)
	}
	defer resp.Body.Close()
	// 4. 检查上游状态码，读取回复
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	const maxResponseBytes = 1 << 20 // 1 MiB

	res, err := io.ReadAll(
		io.LimitReader(resp.Body, maxResponseBytes+1),
	)
	if err != nil {
		return "", fmt.Errorf("read upstream response: %w", err)
	}
	if len(res) > maxResponseBytes {
		return "", fmt.Errorf(
			"upstream response exceeds %d bytes",
			maxResponseBytes,
		)
	}
	// 模拟服务返回的是 JSON 字符串，需要解码去掉 JSON 引号。
	var message string
	if err := json.Unmarshal(res, &message); err != nil {
		return "", fmt.Errorf("decode upstream response: %w", err)
	}

	// 5. 返回回复字符串或错误
	return message, nil
}
