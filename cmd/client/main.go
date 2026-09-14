package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	// 1. 准备要发送的 JSON
	body := strings.NewReader(`{"message":"我发出来了"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 2. 构造请求：方法、地址、请求体
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://localhost:8080/echo",
		body,
	)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 3. 发出请求，等待响应
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	// 4. 读取服务端返回的正文
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("状态码：", resp.StatusCode)
	fmt.Println("响应正文：", string(data))
}
