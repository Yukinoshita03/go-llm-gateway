package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestBodySizeLimit(t *testing.T) {
	const limit = 64 << 10
	const prefix = `{"message":"`
	const suffix = `"}`
	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"below limit", prefix + strings.Repeat("a", limit-1-len(prefix)-len(suffix)) + suffix, http.StatusOK},
		{"exactly at limit", prefix + strings.Repeat("a", limit-len(prefix)-len(suffix)) + suffix, http.StatusOK},
		{"one byte over limit", prefix + strings.Repeat("a", limit+1-len(prefix)-len(suffix)) + suffix, http.StatusRequestEntityTooLarge},
		{"invalid json", `{bad json}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := make(chan ChatRequest, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode forwarded request: %v", err)
				}
				select {
				case calls <- req:
				default:
					t.Error("unexpected extra upstream call")
				}
				_ = json.NewEncoder(w).Encode("ok")
			}))
			defer provider.Close()
			client := provider.Client()
			client.Timeout = 2 * time.Second
			request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			forwardEcho(recorder, request, &HTTPProvider{Client: client, URL: provider.URL})
			if recorder.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d; body: %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				select {
				case got := <-calls:
					want := strings.TrimSuffix(strings.TrimPrefix(tc.body, prefix), suffix)
					if got.Model != "mock-model" || len(got.Messages) != 1 || got.Messages[0].Content != want {
						t.Fatal("forwarded message differs from input")
					}
				default:
					t.Fatal("valid request did not reach upstream")
				}
			} else {
				if tc.wantStatus == http.StatusRequestEntityTooLarge {
					assertJSONError(t, recorder, "request_body_too_large", "request body too large")
				} else {
					assertJSONError(t, recorder, "invalid_json", "invalid json")
				}
				select {
				case <-calls:
					t.Fatal("rejected request reached upstream")
				default:
				}
			}
		})
	}
}

func TestUpstreamResponseSizeLimit(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name       string
		bodyBytes  int
		wantStatus int
	}{
		{"below limit", limit - 1, http.StatusOK},
		{"exactly at limit", limit, http.StatusOK},
		{"one byte over limit", limit + 1, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// JSON 外层两个引号也计入正文大小；ASCII 字符各占一个字节。
			message := strings.Repeat("a", tc.bodyBytes-2)
			body := `"` + message + `"`
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer provider.Close()
			client := provider.Client()
			client.Timeout = 2 * time.Second

			got, err := (&HTTPProvider{Client: client, URL: provider.URL}).Complete(context.Background(), ChatRequest{Model: "mock-model", Messages: []ChatMessage{{Role: "user", Content: "hello"}}})
			if tc.wantStatus == http.StatusOK {
				if err != nil || got != message {
					t.Fatalf("got message length %d, error %v; want length %d", len(got), err, len(message))
				}
			} else if err == nil || !strings.Contains(err.Error(), "upstream response exceeds") || got != "" {
				t.Fatalf("got message length %d, error %v; want size-limit error", len(got), err)
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`))
			forwardEcho(recorder, request, &HTTPProvider{Client: client, URL: provider.URL})
			if recorder.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestDownstreamCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	handlerDone := make(chan struct{})
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read upstream request: %v", err)
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer provider.Close()

	// 两段请求都走真实的临时 HTTP 服务，验证客户端取消会传到上游。
	upstream := provider.Client()
	defer upstream.CloseIdleConnections()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		forwardEcho(w, r, &HTTPProvider{Client: upstream, URL: provider.URL})
	}))
	defer gateway.Close()
	// 清理顺序：取消客户端、释放模拟模型，再关闭两个服务。
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/echo", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := gateway.Client()
	defer client.CloseIdleConnections()
	result := make(chan error, 1)
	go func() {
		resp, err := client.Do(request)
		if resp != nil {
			resp.Body.Close()
		}
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive the request")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got client error %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not return after cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not observe cancellation")
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway handler did not return after cancellation")
	}
}

func TestUpstreamTimeout(t *testing.T) {
	canceled := make(chan struct{})
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读完请求体，再模拟上游一直不返回响应。
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read upstream request: %v", err)
			return
		}
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
			// 即使测试失败，也让模拟服务退出，避免清理时挂住。
		}
	}))
	defer provider.Close()
	defer close(release)

	client := provider.Client()
	client.Timeout = 100 * time.Millisecond
	request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`))
	recorder := httptest.NewRecorder()

	forwardEcho(recorder, request, &HTTPProvider{Client: client, URL: provider.URL})

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("got status %d, want 504; body: %s", recorder.Code, recorder.Body.String())
	}
	assertJSONError(t, recorder, "upstream_timeout", "upstream timeout")
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not observe request cancellation")
	}
}

func TestUpstreamBodyTimeout(t *testing.T) {
	flushed := make(chan struct{})
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read upstream request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// 发出未闭合的 JSON 字符串，让 Decoder 必须继续等正文。
		if _, err := io.WriteString(w, `"partial`); err != nil {
			t.Errorf("write partial response: %v", err)
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush partial response: %v", err)
			return
		}
		close(flushed)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer provider.Close()
	defer close(release)

	client := provider.Client()
	client.Timeout = 200 * time.Millisecond
	request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`))
	recorder := httptest.NewRecorder()
	forwardEcho(recorder, request, &HTTPProvider{Client: client, URL: provider.URL})

	select {
	case <-flushed:
	default:
		t.Fatal("upstream did not flush headers and partial body")
	}
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("got status %d, want 504; body: %s", recorder.Code, recorder.Body.String())
	}
	assertJSONError(t, recorder, "upstream_timeout", "upstream timeout")
}

func TestEchoHandler(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/work" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected upstream request: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "mock-model" || len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
			t.Errorf("unexpected upstream body: %+v, error: %v", req, err)
		}
		_ = json.NewEncoder(w).Encode("丁哥")
	}))
	defer provider.Close()
	tests := []struct {
		name        string
		method      string
		body        string
		wantStatus  int
		wantType    string
		wantMessage string
	}{
		{
			name:       "method not allowed",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
			wantType:   "method_not_allowed",
		},
		{
			name:       "invalid json",
			method:     http.MethodPost,
			body:       `{bad json}`,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_json",
		},
		{
			name:       "empty message",
			method:     http.MethodPost,
			body:       `{"message":""}`,
			wantStatus: http.StatusBadRequest,
			wantType:   "message_required",
		},
		{
			name:        "success",
			method:      http.MethodPost,
			body:        `{"message":"hello"}`,
			wantStatus:  http.StatusOK,
			wantMessage: "丁哥",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(
				testCase.method,
				"/echo",
				strings.NewReader(testCase.body),
			)

			request.Header.Set("Content-Type", "application/json")

			recorder := httptest.NewRecorder()

			forwardEcho(recorder, request, &HTTPProvider{Client: provider.Client(), URL: provider.URL + "/work"})

			if recorder.Code != testCase.wantStatus {
				t.Fatalf(
					"got status %d, want %d",
					recorder.Code,
					testCase.wantStatus,
				)
			}

			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("got Content-Type %q, want application/json", got)
			}

			if testCase.wantType != "" {
				var response ErrorResponse
				if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if response.Error.Type != testCase.wantType {
					t.Fatalf("got error type %q, want %q", response.Error.Type, testCase.wantType)
				}
				return
			}

			var response EchoResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode success response: %v", err)
			}
			if response.Message != testCase.wantMessage {
				t.Fatalf("got message %q, want %q", response.Message, testCase.wantMessage)
			}
		})
	}
}

func TestNewMuxRoutesEcho(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/echo",
		strings.NewReader(`{"message":"routed"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newMux(providerFunc(func(context.Context, ChatRequest) (string, error) {
		t.Fatal("GET should not call provider")
		return "", nil
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, "failed"},
		{"invalid json", http.StatusOK, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer provider.Close()
			recorder := httptest.NewRecorder()
			forwardEcho(recorder, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`)), &HTTPProvider{Client: provider.Client(), URL: provider.URL})
			assertJSONError(t, recorder, "upstream_error", "upstream request failed")
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("got %d, want 502", recorder.Code)
			}
		})
	}
	t.Run("connection failed", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		provider.Close()
		recorder := httptest.NewRecorder()
		forwardEcho(recorder, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`)), &HTTPProvider{Client: provider.Client(), URL: provider.URL})
		assertJSONError(t, recorder, "upstream_error", "upstream request failed")
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("got %d, want 502", recorder.Code)
		}
	})
}

func assertJSONError(t *testing.T, recorder *httptest.ResponseRecorder, wantType, wantMessage string) {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("got Content-Type %q, want application/json", got)
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v; body: %s", err, recorder.Body.String())
	}
	if response.Error.Type != wantType || response.Error.Message != wantMessage {
		t.Fatalf("got error %+v, want type %q, message %q", response.Error, wantType, wantMessage)
	}
}

// 函数适配成 Provider，验证网关可以换成完全不走 HTTP 的实现。
type providerFunc func(context.Context, ChatRequest) (string, error)

func (f providerFunc) Complete(ctx context.Context, req ChatRequest) (string, error) {
	return f(ctx, req)
}

func TestProviderReplacement(t *testing.T) {
	for _, reply := range []string{"模型A的回复", "模型B的回复"} {
		t.Run(reply, func(t *testing.T) {
			called := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := providerFunc(func(gotCtx context.Context, req ChatRequest) (string, error) {
				called = true
				if gotCtx != ctx || req.Model != "mock-model" || len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
					t.Error("request or context was not passed through")
				}
				return reply, nil
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"message":"hello"}`)).WithContext(ctx)
			newMux(provider).ServeHTTP(recorder, request)
			if !called || recorder.Code != http.StatusOK {
				t.Fatalf("called=%v, status=%d", called, recorder.Code)
			}
			var got EchoResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got.Message != reply {
				t.Fatalf("got %+v, error %v; want %q", got, err, reply)
			}
		})
	}
}
