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
