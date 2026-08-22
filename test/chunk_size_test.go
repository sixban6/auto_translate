package test

import (
	"testing"

	"auto_translate/pkg/config"
)

// Chapter-batch targets (runes per request). Larger batches mean fewer
// requests and richer per-chapter context.
func TestAutoCalculateMaxChunkSize(t *testing.T) {
	bigSize := config.AutoCalculateMaxChunkSize("Qwen3.8-27B-oQ4e-mtp")
	if bigSize != 3200 {
		t.Fatalf("Expected 27B model chunk size 3200, got %d", bigSize)
	}
	smallSize := config.AutoCalculateMaxChunkSize("Hy-MT2-1.8B-Abliterated-8bit")
	if smallSize != 2400 {
		t.Fatalf("Expected small model chunk size 2400, got %d", smallSize)
	}
	unknownSize := config.AutoCalculateMaxChunkSize("unknown-model")
	if unknownSize != 2400 {
		t.Fatalf("Expected unknown model chunk size 2400, got %d", unknownSize)
	}
	if tg := config.AutoCalculateMaxChunkSize("translategemma:12b"); tg != 800 {
		t.Fatalf("Expected translategemma chunk size 800, got %d", tg)
	}
}
