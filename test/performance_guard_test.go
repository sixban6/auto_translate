package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

func TestConfig_ConcurrencyCappedByCPUCoresMinusOne(t *testing.T) {
	cfg := &config.Config{
		Model:       "translategemma:12b",
		Concurrency: 999,
	}

	cfg.AutoDetectAndCalculate()

	expectedMax := runtime.NumCPU() - 1
	if expectedMax < 1 {
		expectedMax = 1
	}
	if expectedMax > 4 {
		expectedMax = 4
	}
	if cfg.Concurrency != expectedMax {
		t.Fatalf("expected concurrency capped to %d, got %d", expectedMax, cfg.Concurrency)
	}
}

func TestConfig_ConcurrencyCappedByModelStabilityProfile(t *testing.T) {
	cfg := &config.Config{
		Model:       "qwen3.5:4b",
		Concurrency: 999,
	}
	cfg.AutoDetectAndCalculate()
	if cfg.Concurrency != 3 {
		t.Fatalf("expected qwen model capped to 3, got %d", cfg.Concurrency)
	}
}

func TestTranslator_BypassNonTranslatableText(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "qwen2.5:14b",
		Prompt:            "test prompt",
		RequestTimeoutSec: 1,
		MaxRetries:        1,
	}
	tr := translator.New(cfg)

	input := "https://example.com/assets/cover.png"
	got, status, err := tr.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != translator.StatusFallback {
		t.Fatalf("expected fallback status, got %v", status)
	}
	if got != input {
		t.Fatalf("expected bypassed original text, got %q", got)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("expected no API calls for bypassed text, got %d", requestCount)
	}
}
