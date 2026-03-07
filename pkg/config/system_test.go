package config

import (
	"testing"
)

func TestCalculateConcurrency(t *testing.T) {
	// Case 1: 16GB RAM, 8GB Model on macOS
	// reserved = max(8, 0.2*16) = 8GB
	// available = 16 - 8 = 8GB
	// model_cost = 8 * 1.6 = 12.8GB
	// floor(8 / 12.8) = 0 -> bounds to 1
	c1 := autoCalculateLogic(16*1024*1024*1024, 8*1024*1024*1024, "darwin")
	if c1 != 1 {
		t.Errorf("16GB Mac running 8GB model should yield 1 concurrency, got %d", c1)
	}

	// Case 2: 64GB RAM, 30GB Model on Linux
	// reserved = max(6, 0.15*64) = max(6, 9.6) = 9.6GB
	// available = 64 - 9.6 = 54.4GB
	// model_cost = 30 * 1.6 = 48GB
	// floor(54.4 / 48) = 1
	c2 := autoCalculateLogic(64*1024*1024*1024, 30*1024*1024*1024, "linux")
	if c2 != 1 {
		t.Errorf("64GB Linux running 30GB model should yield 1 concurrency, got %d", c2)
	}

	// Case 3: 64GB RAM, 8GB Model on Linux
	// reserved = 9.6GB. Available = 54.4GB.
	// model_cost = 8 * 1.6 = 12.8
	// floor(54.4 / 12.8) = 4
	c3 := autoCalculateLogic(64*1024*1024*1024, 8*1024*1024*1024, "linux")
	if c3 != 4 {
		t.Errorf("64GB Linux running 8GB model should yield 4 concurrency, got %d", c3)
	}

	// Case 4: Zero size probing
	c4 := autoCalculateLogic(0, 0, "windows")
	if c4 != 1 {
		t.Errorf("Zero probe should fallback to 1, got %d", c4)
	}
}
