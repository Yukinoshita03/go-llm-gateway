package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekStreamRouteForwardsEventsIncrementally(t *testing.T) {
	const firstLine = `data: {"choices":[{"delta":{"content":"first"}}]}`
	const secondLine = `data: {"choices":[{"delta":{"content":"second"}}]}`
	const firstEvent = firstLine + "\n\n"
	const secondEvent = secondLine + "\n\n"
	const doneEvent = "data: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q, want test-key", got)
		}
		var request struct {
			ChatRequest
			Stream *bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Stream == nil || !*request.Stream {
			t.Fatalf("upstream stream = %v, want true", request.Stream)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream ResponseWriter does not support flushing")
		}
		_, _ = io.WriteString(w, firstEvent)
		flusher.Flush()
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, secondEvent)
		flusher.Flush()
		_, _ = io.WriteString(w, doneEvent)
		flusher.Flush()
	}))
	defer upstream.Close()

	provider := &DeepSeekProvider{
		Client: upstream.Client(),
		URL:    upstream.URL + "/chat/completions",
		APIKey: "test-key",
	}
	gateway := httptest.NewServer(newMux(provider))
	defer gateway.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, data)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != firstLine+"\n" {
		t.Fatalf("first line = %q, want first event", line)
	}
	if line, err = reader.ReadString('\n'); err != nil || line != "\n" {
		t.Fatalf("first event terminator = %q, err = %v", line, err)
	}

	waitStart := time.Now()
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(waitStart); elapsed < 80*time.Millisecond {
		t.Fatalf("second event arrived after %s; gateway likely buffered the stream", elapsed)
	}
	if line != secondLine+"\n" {
		t.Fatalf("second line = %q, want second event", line)
	}
}
