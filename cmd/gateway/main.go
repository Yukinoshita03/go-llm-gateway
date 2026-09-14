package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, errorType, message string) {
	if err := writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Type: errorType, Message: message},
	}); err != nil {
		log.Println("write error response:", err)
	}
}

type EchoRequest struct {
	Message string `json:"message"`
}

type EchoResponse struct {
	Message string `json:"message"`
}

func forwardEcho(w http.ResponseWriter, r *http.Request, provider Provider) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

	var req EchoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}

	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message_required", "message is required")
		return
	}
	message, err := provider.Complete(r.Context(), ChatRequest{
		Model: "mock-model",
		Messages: []ChatMessage{
			{
				Role:    "user",
				Content: req.Message,
			},
		},
	})
	if err != nil {
		log.Println("upstream request failed:", err)

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
			return
		}

		writeError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		return
	}
	_ = writeJSON(w, http.StatusOK, EchoResponse{
		Message: message,
	})
}

func newMux(provider Provider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		forwardEcho(w, r, provider)
	})
	mux.HandleFunc("/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) {
			chatHandler(w, r, provider)
		},
	)
	mux.HandleFunc("GET /stream-demo", streamDemo)
	return mux
}
func streamDemo(w http.ResponseWriter, r *http.Request) {
	// 确认当前 ResponseWriter 支持立即刷新缓冲区。
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	for _, text := range []string{"你好", "，我是", "你的 Go 助手。"} {
		// SSE 的一条事件：data: 内容 + 两个换行。
		if _, err := fmt.Fprintf(w, "data: %s\n\n", text); err != nil {
			return
		}

		// 把当前缓冲的数据向客户端发送，不等 handler 返回。
		flusher.Flush()

		// 模拟模型分段生成；客户端取消时及时退出。
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func main() {
	provider, err := configuredProvider()
	if err != nil {
		log.Fatal(err)
	}
	log.Println("listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", newMux(provider)))
}
