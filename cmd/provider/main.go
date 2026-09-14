package main

import (
	"encoding/json"
	"log"
	"net/http"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

func workHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages required", http.StatusBadRequest)
		return
	}
	last := req.Messages[len(req.Messages)-1]
	reply := "模拟模型收到：" + last.Content
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reply); err != nil {
		log.Println("write model response:", err)
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/work", workHandler)
	log.Println("provider listening on http://localhost:8081")
	log.Fatal(http.ListenAndServe(":8081", mux))
}
