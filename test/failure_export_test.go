package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

func TestFailure_LogFile_Generation(t *testing.T) {
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
				break
			}
		}

		if strings.Contains(text, "Fail") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[T]" + text}},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "dummy",
		MaxChunkSize:      500,
		Concurrency:       1,
		MaxRetries:        1,
		RequestTimeoutSec: 1,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: strings.Repeat("Good text to process. ", 10)},
		{ID: "2", OriginalText: strings.Repeat("Fail text to trigger error. ", 10)},
	}

	_, stats, err := proc.Process(blocks, nil)
	if err != nil {
		t.Fatalf("Expected no error from Process, got %v", err)
	}

	if stats.FailureCount != 1 {
		t.Errorf("Expected 1 failure, got %d", stats.FailureCount)
	}

	if len(stats.FailedBlocks) != 1 {
		t.Fatalf("Expected 1 failed block recorded, got %d", len(stats.FailedBlocks))
	}

	failedBlock := stats.FailedBlocks[0]
	if failedBlock.ID != "2-0" && failedBlock.ID != "2" {
		t.Errorf("Unexpected failed block ID: %s", failedBlock.ID)
	}
	if !strings.Contains(failedBlock.OriginalText, "Fail text") {
		t.Errorf("Unexpected failed block text: %s", failedBlock.OriginalText)
	}
	if failedBlock.Error == "" {
		t.Errorf("Expected error string to be populated")
	}
}
