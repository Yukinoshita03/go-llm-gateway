package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
)

// ChatMessage 表示一条消息；当前只支持纯文本内容。
type ChatMessage struct {
	// Role 是消息发送者的角色，例如 system、user 或 assistant。
	Role string `json:"role"`
	// Content 是这条消息的文本正文。
	Content string `json:"content"`
}

// ChatRequest 表示客户端提交的一次聊天请求。
type ChatRequest struct {
	// Model 是希望调用的模型名称，当前直接传给上游。
	Model string `json:"model"`
	// Messages 按对话顺序排列；多轮聊天需由调用方把历史消息一起传入。
	Messages []ChatMessage `json:"messages"`
	// Stream 为 true 时要求网关以 SSE 形式逐段返回模型输出。
	Stream bool `json:"stream"`
}

// ChatChoice 表示同一次请求的一份候选回答，不是一轮历史对话。
type ChatChoice struct {
	// Index 是上游给出的候选编号，通常从 0 开始，不表示质量排名。
	Index int `json:"index"`
	// Message 是该候选的完整回复；不是流式响应中的增量片段。
	Message ChatMessage `json:"message"`
	// FinishReason 是生成结束原因，例如 stop 表示正常结束，length 表示达到长度限制。
	FinishReason string `json:"finish_reason"`
}

// ChatResponse 表示非流式聊天响应，保留当前支持的上游字段。
type ChatResponse struct {
	// ID 是上游生成的响应标识，可用于关联一次模型响应。
	ID string `json:"id,omitempty"`
	// Object 是响应对象类型，非流式聊天通常为 chat.completion。
	Object string `json:"object,omitempty"`
	// Created 是上游返回的创建时间，单位为 Unix 秒，不是请求耗时。
	Created int64 `json:"created,omitempty"`
	// Usage 保存上游用量 JSON（如输入、生成和总 token 数），不自行估算。
	// RawMessage 让不同上游的细分字段得以保留；omitempty 在未提供时省略该字段。
	Usage json.RawMessage `json:"usage,omitempty"`
	// Model 是上游实际返回的模型标识，可能与请求中的模型别名不同。
	Model string `json:"model"`
	// Choices 是候选回答列表；当前调用通常只有一项，多项也不代表多轮对话。
	Choices []ChatChoice `json:"choices"`
}

// streamResponseWriter 在每次写出数据后刷新 HTTP 输出缓冲。
type streamResponseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func (w *streamResponseWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	if n > 0 {
		w.started = true
		w.flusher.Flush()
	}
	return n, err
}

func chatHandler(w http.ResponseWriter, r *http.Request, provider Provider) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed,
			"method_not_allowed", "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

	var req ChatRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				"request_body_too_large", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages_required", "messages must not be empty")
		return
	}
	if req.Stream {
		streamChatHandler(w, r, provider, req)
		return
	}
	var response ChatResponse
	var err error
	if chatProvider, ok := provider.(ChatProvider); ok {
		response, err = chatProvider.CompleteChat(r.Context(), req)
	} else {
		var message string
		message, err = provider.Complete(r.Context(), req)
		response = ChatResponse{Model: req.Model, Choices: []ChatChoice{{Index: 0, Message: ChatMessage{Role: "assistant", Content: message}, FinishReason: "stop"}}}
	}
	if err != nil {
		var neterror net.Error
		if errors.As(err, &neterror) && neterror.Timeout() {
			writeError(w, http.StatusGatewayTimeout,
				"upstream_timeout", "upstream timeout")
			return
		}
		writeError(w, http.StatusBadGateway,
			"upstream_error", "upstream request failed")
		return
	}
	_ = writeJSON(w, http.StatusOK, response)
}

func streamChatHandler(w http.ResponseWriter, r *http.Request, provider Provider, req ChatRequest) {
	streamProvider, ok := provider.(StreamProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming_unsupported", "streaming is not supported")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	streamWriter := &streamResponseWriter{w: w, flusher: flusher}
	if err := streamProvider.StreamChat(r.Context(), req, streamWriter); err != nil {
		// 一旦写出过正文，状态码和普通JSON错误都不能再修改；结束流即可。
		if streamWriter.started {
			log.Printf("stream provider failed after response started: %v", err)
			return
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		return
	}
}

var _ io.Writer = (*streamResponseWriter)(nil)
