package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

// glossaryEchoCfg mirrors a realistic Chinese-translation setup with the
// same glossary the user reported.
func glossaryEchoCfg(serverURL string) *config.Config {
	return &config.Config{
		APIURL:            serverURL,
		Engine:            "omlx",
		Model:             "Hy-MT2-7B-4bit",
		Prompt:            "你是专业翻译，将英文译为中文。",
		Glossary:          map[string]string{"Ventas": "抛压", "Selling": "抛压", "Demand": "需求"},
		Temperature:       0.1,
		RequestTimeoutSec: 5,
		MaxRetries:        1,
	}
}

// TestTranslator_GlossaryEchoIsFallback reproduces the reported bug: the
// small model answered "PLEASE BEHEAD ME" by echoing the injected glossary
// block verbatim. That answer must be classified as a fallback (keep the
// original, no success) so the retry pass can take over and the garbage is
// never cached as a completed chunk.
func TestTranslator_GlossaryEchoIsFallback(t *testing.T) {
	echo := "[术语表·必须严格遵守] Buying=买盘; Compras=买盘; Demand=需求; El camino de menor resistencia=阻力最小路径; Selling=抛压; Supply=供应; Ventas=抛压。"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": echo}},
			},
		})
	}))
	defer server.Close()

	tr := translator.New(glossaryEchoCfg(server.URL))
	got, status, err := tr.TranslateBatch(context.Background(),
		translator.TranslateRequest{Text: "PLEASE BEHEAD ME"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != translator.StatusFallback {
		t.Fatalf("glossary echo must be a fallback, got status=%q output=%q", status, got)
	}
	if got != "PLEASE BEHEAD ME" {
		t.Fatalf("fallback must keep the source text, got %q", got)
	}
}

// TestTranslator_PartialPromptLeakStripped verifies a real translation that
// also carries a leaked prompt block keeps only the translation.
func TestTranslator_PartialPromptLeakStripped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[术语表·必须严格遵守] Demand=需求。请处决我。"}},
			},
		})
	}))
	defer server.Close()

	tr := translator.New(glossaryEchoCfg(server.URL))
	got, status, err := tr.TranslateBatch(context.Background(),
		translator.TranslateRequest{Text: "PLEASE BEHEAD ME"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != translator.StatusSuccess {
		t.Fatalf("partial leak with real translation should succeed, got status=%q", status)
	}
	if got != "请处决我。" {
		t.Fatalf("leaked block must be stripped, got %q", got)
	}
}

// TestTranslator_FormatEchoIsFallback covers echoing the paragraph-format
// instruction block.
func TestTranslator_FormatEchoIsFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[格式要求] 输入由 3 个段落组成，段落之间以空行分隔。译文必须一一对应地输出相同数量的段落，段落之间同样用一个空行分隔；禁止合并、拆分、增删段落。"}},
			},
		})
	}))
	defer server.Close()

	tr := translator.New(glossaryEchoCfg(server.URL))
	got, status, _ := tr.TranslateBatch(context.Background(),
		translator.TranslateRequest{Text: "First paragraph here.", ParagraphCount: 3}, nil)
	if status != translator.StatusFallback {
		t.Fatalf("format-block echo must be a fallback, got status=%q output=%q", status, got)
	}
	if got != "First paragraph here." {
		t.Fatalf("fallback must keep the source text, got %q", got)
	}
}
