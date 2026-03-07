package test

import (
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
		Model: "translategemma:12b",
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

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "Short"},
		{ID: "2", OriginalText: "Hello world. This is long."},         // "Hello world. " and "This is long."
		{ID: "3", OriginalText: "Supercalifragilisticexpialidocious"}, // Length 34 -> needs 4 splits
	}

	start := time.Now()
	res, err := proc.Process(blocks, nil)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("Processing took %v", elapsed)

	if len(res) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(res))
	}

	// Verify ID preservation
	if res[0].ID != "1" || res[1].ID != "2" || res[2].ID != "3" {
		t.Errorf("ID mapping failed")
	}

	// Verify short text (1 chunk)
	if res[0].TranslatedText != "[T]Short" {
		t.Errorf("Short text failed: %s", res[0].TranslatedText)
	}

	// Verify sentence split.
	// "Hello world. " is 13 chars > 10 chars. So "Hello world. " is split?
	// Wait, our logic: "Hello world. " length is 13.
	// if len(runes) > maxChunk (10), it does a hard split on the sentence!
	// So "Hello worl" (10) and "d. " (3) -> translated to [T]Hello worl[T]d.
	// Then "This is lo" (10) and "ng." (3).
	// The translated text will be exactly the original text but chopped and prefixed with [T]

	expected3 := "[T]Supercalif[T]ragilistic[T]expialidoc[T]ious"
	if res[2].TranslatedText != expected3 {
		t.Errorf("Hard split failed, got %q, expected %q", res[2].TranslatedText, expected3)
	}

	// Just checking if translations are appended without missing any chars
	combined2 := strings.ReplaceAll(res[1].TranslatedText, "[T]", "")
	expected2 := "Hello world.This is long."
	if combined2 != expected2 {
		t.Errorf("Sentence split lost characters/expected mismatch: %q vs %q", combined2, expected2)
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
		Model: "translategemma:12b",
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
	res, err := proc.Process(blocks, func(current, total int, msg string) {
		if strings.Contains(msg, "降级为原文保留") || strings.Contains(msg, "fallback") {
			warningReceived = true
		}
	})

	if err != nil {
		t.Fatalf("Expected no error due to fallback, got %v", err)
	}

	if len(res) != 1 {
		t.Fatalf("Expected 1 result block, got %d", len(res))
	}

	if res[0].TranslatedText != "Fail me" {
		t.Errorf("Fallback failed, expected original text, got %q", res[0].TranslatedText)
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
		Model: "translategemma:12b",
		MaxChunkSize:      100,
		Concurrency:       5,
		RequestTimeoutSec: 2,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "C1"},
		{ID: "2", OriginalText: "C2"},
		{ID: "3", OriginalText: "C3"},
		{ID: "4", OriginalText: "C4"},
		{ID: "5", OriginalText: "C5"},
	}

	timeoutCount := 0
	_, err := proc.Process(blocks, func(current, total int, msg string) {
		if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout exceeded") || strings.Contains(msg, "API request failed") || strings.Contains(msg, "Retrying") {
			timeoutCount++
		}
	})

	if err != nil {
		t.Fatalf("Process shouldn't return error due to fallback: %v", err)
	}

	if timeoutCount == 0 {
		t.Errorf("Expected timeouts due to serial queueing blocking beyond the HTTP timeout, but none occurred.")
	}
}
