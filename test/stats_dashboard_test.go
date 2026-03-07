package test

import (
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

// We want Translate() to return an extra status indicator to track stats.
// Statuses: Success, Fallback (Glossary/Short), Refused, Failed.
func TestTranslationStats_Tracking(t *testing.T) {
	cfg := &config.Config{
		Model:             "translategemma:12b",
		APIURL:            "http://dummy",
		MaxRetries:        1,
		RequestTimeoutSec: 1,
		Glossary: map[string]string{
			"Term": "术语",
		},
	}
	tr := translator.New(cfg)

	// Since we haven't implemented mock server for this specific test, we'll just test the Short/Glossary logic which doesn't hit the server.

	// 1. Fallback (Glossary)
	res, status, err := tr.Translate("Term")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if status != translator.StatusFallback {
		t.Errorf("Expected status Fallback, got %v", status)
	}
	if res != "术语" {
		t.Errorf("Expected '术语', got %v", res)
	}

	// 2. Fallback (Short Text)
	res2, status2, err2 := tr.Translate("Abc")
	if err2 != nil {
		t.Fatalf("Unexpected error: %v", err2)
	}
	if status2 != translator.StatusFallback {
		t.Errorf("Expected status Fallback, got %v", status2)
	}
	if res2 != "Abc" {
		t.Errorf("Expected 'Abc', got %v", res2)
	}
}
