package test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// TestRealModel_LoopUntranslatedParagraph runs the exact paragraph the user
// saw left untranslated through the full pipeline against a real local
// engine, repeatedly, and verifies every round returns an actual
// translation instead of the original text.
//
// Gated behind AUTOTRANS_E2E=1 so it never runs in the normal suite.
// Env knobs: AUTOTRANS_E2E_MODEL (default Hy-MT2-1.8B-Abliterated-8bit),
// AUTOTRANS_E2E_ROUNDS (default 10), AUTOTRANS_E2E_KEY (default: auto-read
// from ~/.omlx/settings.json).
func TestRealModel_LoopUntranslatedParagraph(t *testing.T) {
	if os.Getenv("AUTOTRANS_E2E") != "1" {
		t.Skip("set AUTOTRANS_E2E=1 to run against the real local engine")
	}
	rounds := 10
	if n := os.Getenv("AUTOTRANS_E2E_ROUNDS"); n != "" {
		fmt.Sscanf(n, "%d", &rounds)
	}
	apiKey := os.Getenv("AUTOTRANS_E2E_KEY")
	if apiKey == "" {
		apiKey = config.ReadOMLXAPIKey()
	}
	model := os.Getenv("AUTOTRANS_E2E_MODEL")
	if model == "" {
		model = "Hy-MT2-1.8B-Abliterated-8bit"
	}
	promptBytes, err := os.ReadFile("../prompts/反脆弱翻译专家.md")
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}

	target := "If about everything top-down fragilizes and blocks antifragility and growth, everything bottom-up thrives under the right amount of stress and disorder. The process of discovery (or innovation, or technological progress) itself depends on antifragile tinkering, aggressive risk bearing rather than formal education."

	blocks := []parser.TextBlock{
		{ID: "1", ChapterID: "ch1", ChapterTitle: "Chapter 1", OriginalText: "Some things benefit from shocks; they thrive and grow when exposed to volatility, randomness, disorder, and stressors and love adventure, risk, and uncertainty."},
		{ID: "2", ChapterID: "ch1", ChapterTitle: "Chapter 1", OriginalText: target},
		{ID: "3", ChapterID: "ch1", ChapterTitle: "Chapter 1", OriginalText: "We have been fragilized top-down, by those in suits who fragilize us while claiming to have our best interests at heart."},
	}

	untranslated := 0
	retryRounds := 0
	for round := 1; round <= rounds; round++ {
		cfg := &config.Config{
			APIURL:            "http://127.0.0.1:8000/v1",
			Engine:            config.EngineOmlx,
			APIKey:            apiKey,
			Model:             model,
			Temperature:       0.1,
			MaxChunkSize:      2000,
			Concurrency:       2,
			MaxRetries:        5,
			RequestTimeoutSec: 300,
			Prompt:            string(promptBytes),
		}
		proc := processor.New(cfg, translator.New(cfg))

		var msgs []string
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		out, stats, err := proc.Process(ctx, blocks, nil, func(c, total int, msg string) {
			if msg != "" {
				msgs = append(msgs, msg)
			}
		}, nil)
		cancel()
		if err != nil {
			t.Fatalf("round %d: Process: %v", round, err)
		}

		bad := 0
		for i, b := range blocks {
			got := strings.TrimSpace(out[i].TranslatedText)
			if got == strings.TrimSpace(b.OriginalText) {
				bad++
				t.Errorf("round %d: block %d left untranslated: %q", round, i, runePreview(got, 80))
			} else if !translator.ContainsHan(got) {
				// The role prompt targets Chinese; a Han-less "translation"
				// of English text is a same-language rewrite, not a
				// translation.
				bad++
				t.Errorf("round %d: block %d not translated to Chinese: %q", round, i, runePreview(got, 80))
			}
		}
		if bad > 0 {
			untranslated += bad
			t.Logf("round %d messages:\n%s", round, strings.Join(msgs, "\n"))
		}
		sawRetry := false
		for _, m := range msgs {
			if strings.Contains(m, "🔁") {
				sawRetry = true
				break
			}
		}
		if sawRetry {
			retryRounds++
		}
		t.Logf("round %d: stats success=%d fallback=%d refused=%d failure=%d | retry-pass=%v | target=%q",
			round, stats.SuccessCount, stats.FallbackCount, stats.RefusedCount, stats.FailureCount, sawRetry,
			strings.TrimSpace(out[1].TranslatedText))
	}
	t.Logf("summary: %d rounds, %d with retry pass, %d untranslated blocks", rounds, retryRounds, untranslated)
	if untranslated > 0 {
		t.Errorf("BUG NOT FIXED: %d untranslated block(s) across %d rounds", untranslated, rounds)
	}
}

func runePreview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
