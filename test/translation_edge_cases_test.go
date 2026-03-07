package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// 1. 短文本兜底策略测试 (Short text fallback test)
func TestTranslator_ShortTextFallback(t *testing.T) {
	// Setup httptest server that panics if hit, because short texts should be resolved via Glossary
	// or returned as-is without network calls.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("API should not be called for short exact glossary matches or extremely short texts")
	}))
	defer server.Close()

	cfg := &config.Config{
		Model:             "translategemma:12b",
		APIURL:            server.URL,
		RequestTimeoutSec: 10,
		MaxRetries:        1,
		Glossary: map[string]string{
			"Portadilla": "扉页",
		},
	}
	tr := translator.New(cfg)

	// Sub-test 1: Short text exists in glossary
	res, _, err := tr.Translate("Portadilla")
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if res != "扉页" {
		t.Errorf("Expected '扉页', got '%v'", res)
	}

	// Sub-test 2: Extremely short text not in glossary (fallback to original text)
	res2, _, err := tr.Translate("Xyz")
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if res2 != "Xyz" {
		t.Errorf("Expected 'Xyz', got '%v'", res2)
	}
}

// 2. 模型拒答拦截测试 (Model refusal interception test)
func TestTranslator_ModelRefusalIntercept(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "请提供需要翻译的文本。", // Refusal message
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	cfg := &config.Config{
		Model:             "translategemma:12b",
		APIURL:            server.URL,
		RequestTimeoutSec: 10,
		MaxRetries:        1,
	}
	tr := translator.New(cfg)

	originalText := "Weird isolated phrase"
	res, _, err := tr.Translate(originalText)
	if err == nil {
		t.Fatalf("Expected error due to model refusal, but got nil")
	}
	// It should fallback to original text safely when marked as error.
	if res != originalText {
		t.Errorf("Expected fallback to original text '%s', got '%v'", originalText, res)
	}
}

// 3. Epub 文本合并测试 (Context Aggregation test)
func TestChunkAggregation_ShortTexts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)

		content := ""
		for _, m := range payload.Messages {
			if m.Role == "user" {
				content = m.Content
			}
		}

		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "Translated: " + content,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	cfg := &config.Config{
		MaxChunkSize:      1000,
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		RequestTimeoutSec: 10,
		MaxRetries:        1,
		Concurrency:       1,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	// Simulate blocks from the same file: very short texts should be merged.
	blocks := []parser.TextBlock{
		{ID: "ch1.html_node_1", OriginalText: "Chapter 1"},
		{ID: "ch1.html_node_2", OriginalText: "Introduction"},
		{ID: "ch1.html_node_3", OriginalText: "This is a long sentence that should not be merged because it's long enough. It actually has some meat to it."},
	}

	translated, _, err := proc.Process(blocks, nil)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}

	// We expect ch1.html_node_1 to contain the translated merged text: "Translated: Chapter 1 Introduction"
	// and node_2 to have a special skip token or be handled such that it doesn't cause issues.
	// We'll check the Output blocks.
	transMap := make(map[string]string)
	for _, b := range translated {
		t.Logf("Result ID: %s, Text: %q", b.ID, b.TranslatedText)
		transMap[b.ID] = b.TranslatedText
	}

	expectedMergedText := "Translated: Chapter 1 Introduction This is a long sentence that should not be merged because it's long enough. It actually has some meat to it."
	if transMap["ch1.html_node_1"] != expectedMergedText {
		t.Errorf("Expected node_1 to merge node_2 and node_3 and translate together, got: %s", transMap["ch1.html_node_1"])
	}

	// For node_2 and node_3, their original text was incorporated into node_1.
	// We expect their translated text to be "<!--merged-->"
	node2Text := transMap["ch1.html_node_2"]
	if node2Text != "<!--merged-->" {
		t.Errorf("Expected node_2 to be merged and thus replaced with an HTML comment, got: %q", node2Text)
	}
	node3Text := transMap["ch1.html_node_3"]
	if node3Text != "<!--merged-->" {
		t.Errorf("Expected node_3 to be merged and thus replaced with an HTML comment, got: %q", node3Text)
	}
}
