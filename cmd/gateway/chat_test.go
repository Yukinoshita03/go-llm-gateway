package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestChatHTTPForwarding(t *testing.T) {
	want := ChatRequest{Model: "mock-model", Messages: []ChatMessage{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，有什么需要帮忙的？"},
		{Role: "user", Content: "解释一下 Go 接口"},
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/work" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var got ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("forwarded conversation: %+v, error: %v; want %+v", got, err, want)
		}
		_ = json.NewEncoder(w).Encode("接口描述一组方法。")
	}))
	defer upstream.Close()
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	newMux(&HTTPProvider{Client: upstream.Client(), URL: upstream.URL + "/work"}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d; body=%s", recorder.Code, recorder.Body.String())
	}
	assertChatResponse(t, recorder, "mock-model", "接口描述一组方法。")
}

func TestChatRoute(t *testing.T) {
	const valid = `{"model":"mock-model","messages":[{"role":"user","content":"你好"}]}`
	for _, tc := range []struct {
		name, method, body string
		status             int
		errorType, message string
	}{
		{"success", http.MethodPost, valid, 200, "", ""},
		{"wrong method", http.MethodGet, "", 405, "method_not_allowed", "method not allowed"},
		{"invalid json", http.MethodPost, `{bad`, 400, "invalid_json", "invalid json"},
		{"missing model", http.MethodPost, `{"messages":[{"role":"user","content":"hi"}]}`, 400, "model_required", "model is required"},
		{"empty model", http.MethodPost, `{"model":"","messages":[{"role":"user","content":"hi"}]}`, 400, "model_required", "model is required"},
		{"missing messages", http.MethodPost, `{"model":"mock-model"}`, 400, "messages_required", "messages must not be empty"},
		{"empty messages", http.MethodPost, `{"model":"mock-model","messages":[]}`, 400, "messages_required", "messages must not be empty"},
		{"oversized", http.MethodPost, `{"model":"mock-model","messages":[{"role":"user","content":"` + strings.Repeat("a", 64<<10) + `"}]}`, 413, "request_body_too_large", "request body too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/v1/chat/completions", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			called := false
			provider := providerFunc(func(ctx context.Context, req ChatRequest) (string, error) {
				called = true
				if req.Model != "mock-model" || len(req.Messages) != 1 || req.Messages[0].Content != "你好" {
					t.Errorf("unexpected provider request: %+v", req)
				}
				return "模拟模型回复：你好", nil
			})
			newMux(provider).ServeHTTP(recorder, request)
			if called != (tc.status == http.StatusOK) {
				t.Errorf("provider called=%v for status %d", called, tc.status)
			}
			if recorder.Code != tc.status {
				t.Fatalf("got status %d, want %d; body: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.errorType != "" {
				// Unmarshal 也检查没有在错误响应后追加另一段 JSON。
				assertJSONError(t, recorder, tc.errorType, tc.message)
				return
			}
			if recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatal("expected application/json")
			}
			assertChatResponse(t, recorder, "mock-model", "模拟模型回复：你好")
		})
	}
}

func assertChatResponse(t *testing.T, recorder *httptest.ResponseRecorder, model, content string) {
	t.Helper()
	// 独立写出 JSON 字段，避免测试与结构体共享错误的 JSON tag。
	want := map[string]any{
		"model": model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}
	var got map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got response %#v, want %#v", got, want)
	}
}
