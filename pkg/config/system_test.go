package config

import (
	"testing"
)

// TestCalculateConcurrency documents the current KV-cache based concurrency
// model: model weights cost 1.1x their size, each concurrent request adds
// ~1.5GB of KV cache, result bounded to [1, 10] (callers apply CPU/model caps).
func TestCalculateConcurrency(t *testing.T) {
	// Case 1: 16GB RAM, 8GB Model on macOS
	// reserved = max(8, 0.2*16) = 8GB; available = 8GB
	// baseModelMem = 8 * 1.1 = 8.8GB > available -> 1
	c1 := autoCalculateLogic(16*1024*1024*1024, 8*1024*1024*1024, "darwin")
	if c1 != 1 {
		t.Errorf("16GB Mac running 8GB model should yield 1 concurrency, got %d", c1)
	}

	// Case 2: 64GB RAM, 30GB Model on Linux
	// reserved = max(6, 0.15*64) = 9.6GB; available = 54.4GB
	// KV room = 54.4 - 33 = 21.4GB -> floor(21.4/1.5) = 14 -> capped at 10
	c2 := autoCalculateLogic(64*1024*1024*1024, 30*1024*1024*1024, "linux")
	if c2 != 10 {
		t.Errorf("64GB Linux running 30GB model should hit the cap of 10, got %d", c2)
	}

	// Case 3: 64GB RAM, 8GB Model on Linux
	// KV room = 54.4 - 8.8 = 45.6GB -> floor(45.6/1.5) = 30 -> capped at 10
	c3 := autoCalculateLogic(64*1024*1024*1024, 8*1024*1024*1024, "linux")
	if c3 != 10 {
		t.Errorf("64GB Linux running 8GB model should hit the cap of 10, got %d", c3)
	}

	// Case 4: Zero size probing
	c4 := autoCalculateLogic(0, 0, "windows")
	if c4 != 1 {
		t.Errorf("Zero probe should fallback to 1, got %d", c4)
	}
}

// TestEstimateModelSizeQuantTokens verifies the name-based size estimator
// recognizes quantization tokens (q4/q8, -mlx) so a "qwen3.8:27b-mlx" style
// Ollama tag is estimated at ~16GB (4-bit) instead of the 54GB fp16 guess.
func TestEstimateModelSizeQuantTokens(t *testing.T) {
	cases := []struct {
		name   string
		wantGB uint64
	}{
		{"qwen3.8:27b-mlx", 16},      // 27 * 0.6
		{"Qwen3.8-27B-oQ4e-mtp", 16}, // "q4" token
		{"llama3:8b-q8", 8},          // 8 * 1.05
		{"mistral:7b", 14},           // fp16 fallback: 7 * 2.0
	}
	for _, tc := range cases {
		got := estimateModelSizeGBFromName(tc.name) / (1024 * 1024 * 1024)
		// Allow rounding slack of 1GB.
		if got < tc.wantGB-1 || got > tc.wantGB+1 {
			t.Errorf("%s: estimated %dGB, want ~%dGB", tc.name, got, tc.wantGB)
		}
	}
}

// TestOllamaUnreachable_FallsBackToEstimate verifies the Ollama path no
// longer degrades to concurrency 1 (or kills the server) when the tags API
// is unreachable: it must return a name-based estimate with a warning, and
// repeated calls must agree (no side effects between runs).
func TestOllamaUnreachable_FallsBackToEstimate(t *testing.T) {
	deadURL := "http://127.0.0.1:1/v1/chat/completions"

	info1, err1 := AutoCalculateConcurrency(deadURL, "qwen3.8:27b-mlx", "ollama")
	info2, err2 := AutoCalculateConcurrency(deadURL, "qwen3.8:27b-mlx", "ollama")
	if err1 != nil || err2 != nil {
		t.Fatalf("unreachable tags API must not surface an error: %v / %v", err1, err2)
	}
	if info1.WarningMsg == "" {
		t.Errorf("expected a fallback warning mentioning the name-based estimate")
	}
	if info1.RecommendedC != info2.RecommendedC || info1.ModelSize != info2.ModelSize {
		t.Errorf("detection must be deterministic across runs: %d/%dGB vs %d/%dGB",
			info1.RecommendedC, info1.ModelSize>>30, info2.RecommendedC, info2.ModelSize>>30)
	}
	// The estimate for a 27B 4-bit model must be ~16GB, not the fp16 54GB.
	if gb := info1.ModelSize >> 30; gb < 15 || gb > 17 {
		t.Errorf("27b-mlx fallback estimate = %dGB, want ~16GB", gb)
	}
	if info1.RecommendedC < 1 || info1.RecommendedC > 4 {
		t.Errorf("recommended concurrency %d out of [1,4] range", info1.RecommendedC)
	}
}
