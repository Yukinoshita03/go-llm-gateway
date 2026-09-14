package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"success", 200, `{"id":"chat-1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"length"}],"usage":{"total_tokens":12}}`, 200},
		{"auth error", 401, `{"error":{"message":"private detail"}}`, 502},
		{"rate limit", 429, `{}`, 502},
		{"invalid json", 200, `broken`, 502},
		{"empty choices", 200, `{"model":"deepseek-v4-flash","choices":[]}`, 502},
		{"oversized", 200, strings.Repeat("x", (1<<20)+1), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect upstream request")
				}
				var req struct {
					ChatRequest
					Stream *bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "deepseek-v4-flash" || len(req.Messages) != 1 || req.Messages[0].Content != "你好" || req.Stream == nil || *req.Stream {
					t.Errorf("incorrect payload: %+v, %v", req, err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			recorder := httptest.NewRecorder()
			newMux(&DeepSeekProvider{Client: server.Client(), URL: server.URL + "/chat/completions", APIKey: "test-key"}).ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}]}`)))
			if recorder.Code != tc.want {
				t.Fatalf("status %d, want %d", recorder.Code, tc.want)
			}
			if tc.want != 200 {
				assertJSONError(t, recorder, "upstream_error", "upstream request failed")
				return
			}
			var got ChatResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != "chat-1" || got.Choices[0].FinishReason != "length" || got.Choices[0].Message.Content != "你好" || string(got.Usage) != `{"total_tokens":12}` {
				t.Fatalf("response fields lost: %+v", got)
			}
		})
	}
}

func TestProviderConfiguration(t *testing.T) {
	t.Setenv("GATEWAY_PROVIDER", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := configuredProvider(); err == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	p, err := configuredProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*DeepSeekProvider); !ok {
		t.Fatal("wrong provider")
	}
	t.Setenv("GATEWAY_PROVIDER", "mock")
	if _, err := configuredProvider(); err != nil {
		t.Fatal(err)
	}
}
