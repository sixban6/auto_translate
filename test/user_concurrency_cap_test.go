package test

import (
	"strings"
	"testing"

	"auto_translate/pkg/config"
)

// TestUserPinnedConcurrency_NotCapped: an explicit user value (e.g. 8) must
// survive auto-detection without being clamped to the CPU/model caps.
func TestUserPinnedConcurrency_NotCapped(t *testing.T) {
	cfg := &config.Config{Model: "qwen:7b", Concurrency: 8}
	cfg.AutoDetectAndCalculate()
	if cfg.Concurrency != 8 {
		t.Errorf("user-pinned 8 must survive, got %d", cfg.Concurrency)
	}
	explain := config.GetConfigExplanation(cfg)
	if strings.Contains(explain, "并发上限已按 CPU 核心约束") {
		t.Errorf("user-pinned value must not emit the CPU-clamp warning: %s", explain)
	}
	if !strings.Contains(explain, "用户指定并发数：8") {
		t.Errorf("explanation must state the pinned value: %s", explain)
	}
}

// TestAutoConcurrency_StillCapped: auto planning (0) keeps its conservative
// caps.
func TestAutoConcurrency_StillCapped(t *testing.T) {
	cfg := &config.Config{Model: "qwen:7b"}
	cfg.AutoDetectAndCalculate()
	if cfg.Concurrency > 4 {
		t.Errorf("auto path must stay capped at 4, got %d", cfg.Concurrency)
	}
}
