package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

type chatRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type chatResponse struct {
	Output string `json:"output"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat", handleChat)

	addr := os.Getenv("EINO_RUNTIME_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("eino runtime listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "local-model"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{
		Output: "local service response from " + model + ": " + prompt,
	})
}
