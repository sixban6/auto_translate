package test

import (
	"testing"

	"auto_translate/pkg/config"
)

func TestAutoCalculateMaxChunkSize(t *testing.T) {
	qwenSize := config.AutoCalculateMaxChunkSize("qwen2.5:32b")
	if qwenSize != 1100 {
		t.Fatalf("Expected qwen2.5 chunk size 1100, got %d", qwenSize)
	}
	qwen35Size := config.AutoCalculateMaxChunkSize("qwen3.5:4b")
	if qwen35Size != 700 {
		t.Fatalf("Expected qwen3.5 chunk size 700, got %d", qwen35Size)
	}

	unknownSize := config.AutoCalculateMaxChunkSize("unknown-model")
	if unknownSize != 800 {
		t.Fatalf("Expected unknown model chunk size 800, got %d", unknownSize)
	}
}
