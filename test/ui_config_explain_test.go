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
