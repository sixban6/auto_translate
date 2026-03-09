package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

func TestProcessor(t *testing.T) {
	// Mock Server just echoes back the user message with a simple prefix
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)

		var text string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				text = m.Content
			}
		}

		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "[T]" + text,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		MaxChunkSize:      10, // Very small to force word-level or sentence-level split
		Concurrency:       2,
		Temperature:       0,
		RequestTimeoutSec: 1,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	// Block 1: Short text (no split)
	// Block 2: Long text requiring split by sentence
	// Block 3: Very long word requiring hard split

	b1 := strings.Repeat("A", 250)
	b2 := strings.Repeat("B", 250)
	b3 := strings.Repeat("C", 250)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: b1},
		{ID: "2", OriginalText: b2},
		{ID: "3", OriginalText: b3},
	}

	start := time.Now()
	translatedBlocks, _, err := proc.Process(context.Background(), blocks, nil, nil, nil)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("Processing took %v", elapsed)

	if len(translatedBlocks) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(translatedBlocks))
	}

	// Verify ID preservation
	if translatedBlocks[0].ID != "1" || translatedBlocks[1].ID != "2" || translatedBlocks[2].ID != "3" {
		t.Errorf("ID mapping failed")
	}

	// Verification
	if !strings.Contains(translatedBlocks[0].TranslatedText, "[T]A") {
		t.Errorf("First text chunk failed: %s", translatedBlocks[0].TranslatedText)
	}

	if !strings.Contains(translatedBlocks[2].TranslatedText, "[T]C") {
		t.Errorf("Third chunk failed: %s", translatedBlocks[2].TranslatedText)
	}
}

func TestProcessor_Fallback(t *testing.T) {
	// Mock Server that always fails to trigger fallback
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		MaxChunkSize:      100,
		Concurrency:       1,
		RequestTimeoutSec: 1,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "Fail me"},
	}

	warningReceived := false
	translatedBlocks, _, err := proc.Process(context.Background(), blocks, nil, func(current, total int, msg string) {
		if strings.Contains(msg, "降级为原文保留") || strings.Contains(msg, "fallback") {
			warningReceived = true
		}
	}, nil)

	if err != nil {
		t.Fatalf("Expected no error due to fallback, got %v", err)
	}

	if len(translatedBlocks) != 1 {
		t.Fatalf("Expected 1 result block, got %d", len(translatedBlocks))
	}

	if translatedBlocks[0].TranslatedText != "Fail me" {
		t.Errorf("Fallback failed, expected original text, got %q", translatedBlocks[0].TranslatedText)
	}

	if !warningReceived {
		t.Errorf("Expected fallback warning event to be emitted via onProgress")
	}
}

func TestProcessor_ConcurrencyQueueTimeout(t *testing.T) {
	// Simulate Ollama where requests are processed SERIALLY not in parallel.
	// Each request takes 1 second to process.
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock() // Force strict serial execution like Ollama default
		time.Sleep(1 * time.Second)

		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "Success"}},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	// Timeout is 2.5s. If Concurrency=5, the 3rd request will finish at 3s
	// (which is > 2.5s) so the 3rd, 4th, 5th should TIMEOUT.
	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		MaxChunkSize:      100,
		Concurrency:       5,
		MaxRetries:        1,
		RequestTimeoutSec: 2,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: strings.Repeat("C", 250)},
		{ID: "2", OriginalText: strings.Repeat("C", 250)},
		{ID: "3", OriginalText: strings.Repeat("C", 250)},
		{ID: "4", OriginalText: strings.Repeat("C", 250)},
		{ID: "5", OriginalText: strings.Repeat("C", 250)},
	}

	timeoutCount := 0
	_, _, err := proc.Process(context.Background(), blocks, nil, func(current, total int, msg string) {
		if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout exceeded") || strings.Contains(msg, "API request failed") || strings.Contains(msg, "Retrying") || strings.Contains(msg, "完全失败") {
			timeoutCount++
		}
	}, nil)

	if err != nil {
		t.Fatalf("Process shouldn't return error due to fallback: %v", err)
	}

	if timeoutCount == 0 {
		t.Errorf("Expected timeouts due to serial queueing blocking beyond the HTTP timeout, but none occurred.")
	}
}
