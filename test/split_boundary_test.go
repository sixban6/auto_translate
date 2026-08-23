package test

import (
	"context"
	"encoding/json"
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

// captureBatches runs Process over one oversized block and returns the exact
// user messages (batch texts) the model received.
func captureBatches(t *testing.T, cfg *config.Config, text string) []string {
	t.Helper()
	var mu sync.Mutex
	var users []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		for _, m := range payload.Messages {
			if m.Role == "user" {
				mu.Lock()
				users = append(users, m.Content)
				mu.Unlock()
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[T]译文"}},
			},
		})
	}))
	defer server.Close()
	cfg.APIURL = server.URL

	blocks := []parser.TextBlock{{ID: "b1", OriginalText: text, ChapterID: "c1"}}
	proc := processor.New(cfg, translator.New(cfg))
	if _, _, err := proc.Process(context.Background(), blocks, nil, nil, nil); err != nil {
		t.Fatalf("process: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return users
}

// TestSplitBoundary_AbbreviationNotTorn: an oversized English paragraph must
// never be cut after an abbreviation dot — "Mr. Smith" / "U.S." stay whole
// inside one batch.
func TestSplitBoundary_AbbreviationNotTorn(t *testing.T) {
	cfg := &config.Config{
		Model: "m", MaxChunkSize: 60, Concurrency: 1,
		RequestTimeoutSec: 5, MaxRetries: 1, ChapterBatching: true,
	}
	para := strings.Join([]string{
		"Mr. Smith arrived at the U.S. Capitol early.",
		"Mrs. Jones met Dr. Lee near the entrance.",
		"They discussed the figures shown in Fig. three.",
	}, " ")

	users := captureBatches(t, cfg, para)

	joined := strings.Join(users, "\n===\n")
	for _, keep := range []string{"Mr. Smith", "U.S. Capitol", "Mrs. Jones", "Dr. Lee", "Fig. three"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("abbreviation torn: %q not intact in any single batch:\n%s", keep, joined)
		}
	}
	for _, bad := range []string{"Mr.\n===\n", "U.S.\n===\n", "Dr.\n===\n", "Fig.\n===\n"} {
		if strings.Contains(joined, bad) {
			t.Errorf("batch ends with abbreviation fragment %q", strings.TrimSpace(bad))
		}
	}
}

// TestSplitBoundary_WordNeverHalved: a long separator-free sentence (only
// spaces) must be broken between words — every batch is a sequence of whole
// words, and nothing is lost or duplicated.
func TestSplitBoundary_WordNeverHalved(t *testing.T) {
	cfg := &config.Config{
		Model: "m", MaxChunkSize: 50, Concurrency: 1,
		RequestTimeoutSec: 5, MaxRetries: 1, ChapterBatching: true,
	}
	// One single 200-rune "sentence" (no ., no commas) — only word spaces.
	para := strings.Repeat("alpha bravo charlie delta echo ", 8)

	users := captureBatches(t, cfg, para)

	originalWords := map[string]bool{}
	for _, w := range strings.Fields(para) {
		originalWords[w] = true
	}
	for _, u := range users {
		for _, w := range strings.Fields(u) {
			if !originalWords[w] {
				t.Errorf("batch contains a halved/foreign token %q (batch: %q)", w, u)
			}
		}
	}
	// Nothing lost: all pieces concatenated (minus join spaces) equal the
	// source minus its spaces.
	flatten := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	if got, want := flatten(strings.Join(users, "")), flatten(para); got != want {
		t.Errorf("split lost or duplicated content:\n got %q\nwant %q", got, want)
	}
}

// TestSplitBoundary_WhitespaceFreeHardCut: a whitespace-free run (URL) has
// no safe boundary; the only acceptable behavior is a clean cut with zero
// data loss.
func TestSplitBoundary_WhitespaceFreeHardCut(t *testing.T) {
	cfg := &config.Config{
		Model: "m", MaxChunkSize: 40, Concurrency: 1,
		RequestTimeoutSec: 5, MaxRetries: 1, ChapterBatching: true,
	}
	// A whitespace-free alphanumeric run (not a URL: those are deliberately
	// bypassed untranslated); no boundary exists, so the only acceptable
	// behavior is a clean cut with zero data loss.
	url := "Zq7" + strings.Repeat("xk9", 90)
	users := captureBatches(t, cfg, url)

	if len(users) < 2 {
		t.Fatalf("expected the oversized URL to be split, got %d batches", len(users))
	}
	if got := strings.Join(users, ""); got != url {
		t.Errorf("hard cut lost content:\n got %q\nwant %q", got, url)
	}
}

// TestSplitBoundary_ChineseNoSpuriousSpaces: joining Chinese sentences
// inside one oversized paragraph must not inject Latin spaces between them.
func TestSplitBoundary_ChineseNoSpuriousSpaces(t *testing.T) {
	cfg := &config.Config{
		Model: "m", MaxChunkSize: 40, Concurrency: 1,
		RequestTimeoutSec: 5, MaxRetries: 1, ChapterBatching: true,
	}
	sentence := "这是用于测试的一个较长中文句子，包含足量字符。"
	para := strings.Repeat(sentence, 12) // oversized single paragraph

	users := captureBatches(t, cfg, para)

	for _, u := range users {
		if strings.Contains(u, "。 ") || strings.Contains(u, "， ") {
			t.Errorf("spurious space injected between Chinese sentences: %q", u)
		}
	}
}
