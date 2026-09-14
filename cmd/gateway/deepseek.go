package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ChatProvider 保留上游的结束原因等聊天响应字段。
type ChatProvider interface {
	CompleteChat(context.Context, ChatRequest) (ChatResponse, error)
}

// StreamProvider 描述以 SSE 增量写出模型响应的能力。
type StreamProvider interface {
	StreamChat(context.Context, ChatRequest, io.Writer) error
}

type DeepSeekProvider struct {
	// Client 是复用的出站 HTTP 客户端，负责连接复用、超时及重定向策略。
	Client *http.Client
	// URL 是完整的聊天接口地址；测试时可替换为本地模拟服务器。
	URL string
	// APIKey 是上游鉴权凭据，从环境变量读取，仅用于 Authorization 请求头。
	// 不要将其写入日志、响应或版本控制。
	APIKey string
}

var _ Provider = (*DeepSeekProvider)(nil)
var _ ChatProvider = (*DeepSeekProvider)(nil)
var _ StreamProvider = (*DeepSeekProvider)(nil)

func (p *DeepSeekProvider) Complete(ctx context.Context, req ChatRequest) (string, error) {
	response, err := p.CompleteChat(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Choices[0].Message.Content, nil
}

const maxDeepSeekResponseBytes = 1 << 20

func (p *DeepSeekProvider) CompleteChat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var result ChatResponse
	req.Stream = false
	request, err := p.newRequest(ctx, req)
	if err != nil {
		return result, err
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return result, fmt.Errorf("call DeepSeek: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 不把可能包含敏感信息的上游正文写入日志或下游响应。
		return result, fmt.Errorf("DeepSeek returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDeepSeekResponseBytes+1))
	if err != nil {
		return result, fmt.Errorf("read DeepSeek response: %w", err)
	}
	if len(data) > maxDeepSeekResponseBytes {
		return result, fmt.Errorf("DeepSeek response exceeds %d bytes", maxDeepSeekResponseBytes)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode DeepSeek response: %w", err)
	}
	if result.Model == "" || len(result.Choices) == 0 || result.Choices[0].Message.Role != "assistant" || result.Choices[0].FinishReason == "" {
		return ChatResponse{}, fmt.Errorf("invalid DeepSeek response fields")
	}
	return result, nil
}

func (p *DeepSeekProvider) StreamChat(ctx context.Context, req ChatRequest, dst io.Writer) error {
	req.Stream = true
	request, err := p.newRequest(ctx, req)
	if err != nil {
		return err
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return fmt.Errorf("call DeepSeek stream: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("DeepSeek returned status %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("DeepSeek stream content type is %q", response.Header.Get("Content-Type"))
	}

	_, err = forwardDeepSeekSSE(response.Body, dst, maxDeepSeekResponseBytes)
	if err != nil {
		return fmt.Errorf("read DeepSeek stream: %w", err)
	}
	return nil
}

func (p *DeepSeekProvider) newRequest(ctx context.Context, req ChatRequest) (*http.Request, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode DeepSeek request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create DeepSeek request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+p.APIKey)
	if req.Stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return request, nil
}

func configuredProvider() (Provider, error) {
	switch os.Getenv("GATEWAY_PROVIDER") {
	case "", "mock":
		return &HTTPProvider{Client: &http.Client{Timeout: 2 * time.Second}, URL: "http://localhost:8081/work"}, nil
	case "deepseek":
		key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
		if key == "" {
			return nil, fmt.Errorf("DEEPSEEK_API_KEY is required")
		}

		return &DeepSeekProvider{
			Client: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			URL:    "https://api.deepseek.com/chat/completions", APIKey: key,
		}, nil
	default:
		return nil, fmt.Errorf("GATEWAY_PROVIDER must be mock or deepseek")
	}
}
