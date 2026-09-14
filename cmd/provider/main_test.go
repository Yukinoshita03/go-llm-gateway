package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkHandler(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		status             int
		reply              string
	}{
		{"single message", "POST", `{"model":"mock-model","messages":[{"role":"user","content":"你好"}]}`, 200, "模拟模型收到：你好"},
		{"last message", "POST", `{"model":"mock-model","messages":[{"role":"user","content":"旧问题"},{"role":"assistant","content":"旧回答"},{"role":"user","content":"解释 Go 接口"}]}`, 200, "模拟模型收到：解释 Go 接口"},
		{"invalid json", "POST", `{bad`, 400, ""},
		{"missing messages", "POST", `{}`, 400, ""},
		{"empty messages", "POST", `{"messages":[]}`, 400, ""},
		{"wrong method", "GET", ``, 405, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			workHandler(recorder, httptest.NewRequest(tc.method, "/work", strings.NewReader(tc.body)))
			if recorder.Code != tc.status {
				t.Fatalf("got status %d, want %d", recorder.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				var got string
				if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got != tc.reply {
					t.Fatalf("got reply %q, error %v; want %q", got, err, tc.reply)
				}
				if recorder.Header().Get("Content-Type") != "application/json" {
					t.Fatal("expected JSON content type")
				}
			}
		})
	}
}
