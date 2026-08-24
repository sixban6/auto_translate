package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// headingScenarioServer emulates the real Hy-MT2 behavior seen in the bug:
// it returns exactly one paragraph per requested paragraph (correct model
// behavior), and asserts the request's paragraph count. This only produces
// a correct final document when headings are NOT packed with body text.
func headingScenarioServer(t *testing.T, recv *[][]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		var user string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		paras := strings.Split(strings.TrimSpace(user), "\n\n")
		mu.Lock()
		*recv = append(*recv, paras)
		mu.Unlock()
		// Model answers with exactly one translation per input paragraph.
		out := make([]string, len(paras))
		for i := range paras {
			out[i] = fmt.Sprintf("【%d】%s的译文", i+1, firstWords(paras[i]))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": strings.Join(out, "\n\n")}},
			},
		})
	}))
}

func firstWords(s string) string {
	fields := strings.Fields(s)
	if len(fields) > 3 {
		fields = fields[:3]
	}
	return strings.Join(fields, "-")
}

// TestHeadingOwnBatch_ChapterMode reproduces the reported bug: with chapter
// batching ON, an h2 heading (e.g. "PLEASE BEHEAD ME") was packed into the
// first body batch; a well-behaved model returned exactly N paragraphs, the
// heading got entry 0's slot and every following paragraph shifted by one —
// leaving the heading untranslated and a stray fragment of the first
// paragraph's translation behind it. The heading must now be its own batch.
func TestHeadingOwnBatch_ChapterMode(t *testing.T) {
	var recv [][]string
	server := headingScenarioServer(t, &recv)
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "Hy-MT2-7B-4bit",
		MaxChunkSize: 4800, Concurrency: 5, ChapterBatching: true,
		RequestTimeoutSec: 5, MaxRetries: 1,
	}
	blocks := []parser.TextBlock{
		{ID: "h1", OriginalText: "PLEASE BEHEAD ME", HeadingLevel: 2, ChapterID: "doc"},
		{ID: "p1", OriginalText: "If we have no common name for antifragility, we can find a mythological equivalence, the expression of historical intelligence through potent metaphors.", ChapterID: "doc"},
		{ID: "p2", OriginalText: "In a Roman recycled version of a Greek myth, the Sicilian tyrant Dionysius II has the fawning courtier Damocles enjoy the luxury of a fancy banquet.", ChapterID: "doc"},
	}

	out, _, err := processor.New(cfg, translator.New(cfg)).
		Process(context.Background(), blocks, nil, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	byID := map[string]string{}
	for _, b := range out {
		byID[b.ID] = b.TranslatedText
	}

	// The heading must be translated on its own (never the raw English).
	if !strings.Contains(byID["h1"], "译文") || strings.Contains(byID["h1"], "PLEASE BEHEAD ME") {
		t.Errorf("heading not translated: %q", byID["h1"])
	}
	// Body paragraphs must each hold exactly their own translation.
	for _, id := range []string{"p1", "p2"} {
		if !strings.Contains(byID[id], "译文") {
			t.Errorf("%s missing translation: %q", id, byID[id])
		}
		if strings.Contains(byID[id], "如果我们找不到") {
			t.Errorf("%s carries a fragment of another paragraph: %q", id, byID[id])
		}
	}

	// No request may mix the heading with body paragraphs.
	for _, paras := range recv {
		joined := strings.Join(paras, " ")
		if strings.Contains(joined, "PLEASE BEHEAD ME") && len(paras) > 1 {
			t.Errorf("heading packed with body in one request: %q", paras)
		}
	}
	// The heading request must be single-paragraph.
	for _, paras := range recv {
		joined := strings.Join(paras, " ")
		if strings.Contains(joined, "PLEASE BEHEAD ME") && len(paras) != 1 {
			t.Errorf("heading request must be single-paragraph, got %d", len(paras))
		}
	}
}
