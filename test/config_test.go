package test

import (
	"os"
	"testing"

	"auto_translate/pkg/config"
)

func TestConfigLoad(t *testing.T) {
	// Create a temporary valid config file
	validJSON := `{
		"api_url": "http://localhost",
		"model": "qwen",
		"prompt": "test",
		"input_file": "in.txt",
		"output_file": "out.txt",
		"bilingual": true
	}`
	validFile := "test_valid_config.json"
	os.WriteFile(validFile, []byte(validJSON), 0644)
	defer os.Remove(validFile)

	cfg, err := config.Load(validFile)
	if err != nil {
		t.Fatalf("Failed to load valid config: %v", err)
	}

	if cfg.Concurrency != 1 {
		t.Errorf("Expected default concurrency 1, got %d", cfg.Concurrency)
	}
	if cfg.MaxChunkSize != 600 {
		t.Errorf("Expected default max_chunk_size 600, got %d", cfg.MaxChunkSize)
	}
	if cfg.RequestTimeoutSec != 180 {
		t.Errorf("Expected default request_timeout_sec 180, got %d", cfg.RequestTimeoutSec)
	}
	if cfg.Bilingual != true {
		t.Errorf("Expected bilingual true, got false")
	}

	// Test missing required field
	invalidJSON := `{
		"api_url": "http://localhost",
		"model": "qwen"
	}`
	invalidFile := "test_invalid_config.json"
	os.WriteFile(invalidFile, []byte(invalidJSON), 0644)
	defer os.Remove(invalidFile)

	_, err = config.Load(invalidFile)
	if err == nil {
		t.Error("Expected error for missing required fields, got nil")
	}
}
