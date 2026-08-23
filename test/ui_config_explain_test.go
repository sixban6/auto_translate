package test

import (
	"strings"
	"testing"

	"auto_translate/pkg/config"
)

func TestExplanationLog_Generation(t *testing.T) {
	// Test case 1: "translategemma:12b"
	cfg1 := &config.Config{Model: "translategemma:12b"}
	cfg1.AutoDetectAndCalculate()
	explain1 := config.GetConfigExplanation(cfg1)

	if !strings.Contains(explain1, "探测到") || !strings.Contains(explain1, "建议并发数：") {
		t.Errorf("Explanation 1 missing key tokens: %s", explain1)
	}

	// Test case 2: "qwen:7b"
	cfg2 := &config.Config{Model: "qwen:7b"}
	cfg2.AutoDetectAndCalculate()
	explain2 := config.GetConfigExplanation(cfg2)

	if !strings.Contains(explain2, "并发数：") {
		t.Errorf("Explanation 2 missing key tokens: %s", explain2)
	}

	if explain1 == explain2 {
		t.Errorf("Expected different explanations for different models")
	}
}

// TestConfigExplanation_UserPinnedConcurrency pins the user-override
// semantics: an explicitly set concurrency survives auto-detection and the
// explanation says so (the web UI concurrency selector relies on this).
func TestConfigExplanation_UserPinnedConcurrency(t *testing.T) {
	cfg := &config.Config{Model: "qwen:7b", Concurrency: 2}
	cfg.AutoDetectAndCalculate()
	if cfg.Concurrency != 2 {
		t.Errorf("user-pinned concurrency must survive auto-detection, got %d", cfg.Concurrency)
	}
	explain := config.GetConfigExplanation(cfg)
	if !strings.Contains(explain, "用户指定并发数：2") {
		t.Errorf("explanation must state the user-pinned value: %s", explain)
	}
}

// TestConfigExplanation_AutoConcurrency labels the auto path.
func TestConfigExplanation_AutoConcurrency(t *testing.T) {
	cfg := &config.Config{Model: "qwen:7b"}
	cfg.AutoDetectAndCalculate()
	if cfg.Concurrency < 1 {
		t.Errorf("auto path must produce a usable concurrency >= 1, got %d", cfg.Concurrency)
	}
	explain := config.GetConfigExplanation(cfg)
	if !strings.Contains(explain, "并发数：") {
		t.Errorf("explanation missing concurrency info: %s", explain)
	}
}
