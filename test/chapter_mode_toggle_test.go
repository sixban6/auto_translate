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

// chapterToggleServer records every user message the model receives and
// answers with "[T]"+input.
func chapterToggleServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var userMsgs []string
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
				userMsgs = append(userMsgs, m.Content)
				mu.Unlock()
			} else if m.Role == "system" {
				mu.Lock()
				userMsgs = append(userMsgs, "SYSTEM:"+m.Content)
				mu.Unlock()
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[T]已翻译"}},
			},
		})
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), userMsgs...)
	}
}

func chapterBlocks() []parser.TextBlock {
	return []parser.TextBlock{
		{ID: "c1_1", OriginalText: "First paragraph of chapter one.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
		{ID: "c1_2", OriginalText: "Second paragraph of chapter one.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
		{ID: "c2_1", OriginalText: "First paragraph of chapter two.", ChapterID: "ch2", ChapterTitle: "Chapter Two"},
	}
}

// TestChapterMode_Off_ClassicPerBlock verifies the default (toggle off)
// reproduces the pre-chapter behavior: one request per paragraph, no
// chapter grouping, no chapter title or rolling context in the prompt, and
// legacy-style state keys ("{block}-0") for checkpoint compatibility.
func TestChapterMode_Off_ClassicPerBlock(t *testing.T) {
	server, msgs := chapterToggleServer(t)
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "m",
		MaxChunkSize: 2400, Concurrency: 2, RequestTimeoutSec: 5, MaxRetries: 1,
	}
	proc := processor.New(cfg, translator.New(cfg))

	var stateKeys []string
	_, _, err := proc.Process(context.Background(), chapterBlocks(), nil, nil,
		func(id, _ string) { stateKeys = append(stateKeys, id) })
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(stateKeys) != 3 {
		t.Fatalf("classic mode must emit one checkpoint per block, got %v", stateKeys)
	}
	for i, want := range []string{"c1_1-0", "c1_2-0", "c2_1-0"} {
		if stateKeys[i] != want {
			t.Errorf("state key %d = %q, want %q (legacy format)", i, stateKeys[i], want)
		}
	}

	// Three paragraphs → three user requests, each exactly one paragraph.
	users := 0
	for _, m := range msgs() {
		if !strings.HasPrefix(m, "SYSTEM:") {
			users++
			if strings.Contains(m, "First paragraph") && strings.Contains(m, "Second paragraph") {
				t.Errorf("paragraphs of one chapter were merged in classic mode: %q", m)
			}
		}
	}
	if users != 3 {
		t.Fatalf("expected 3 translation requests, got %d", users)
	}
	for _, m := range msgs() {
		if strings.HasPrefix(m, "SYSTEM:") {
			if strings.Contains(m, "[当前章节]") || strings.Contains(m, "[前文译文结尾") {
				t.Errorf("classic mode prompt carries chapter context: %q", m)
			}
		}
	}
}

// TestChapterMode_On_BatchesByChapter verifies the toggle-on path keeps the
// current behavior: same-chapter paragraphs share a request and the prompt
// carries the chapter title.
func TestChapterMode_On_BatchesByChapter(t *testing.T) {
	server, msgs := chapterToggleServer(t)
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "m",
		MaxChunkSize: 2400, Concurrency: 1, RequestTimeoutSec: 5, MaxRetries: 1,
		ChapterBatching: true,
	}
	proc := processor.New(cfg, translator.New(cfg))

	if _, _, err := proc.Process(context.Background(), chapterBlocks(), nil, nil, nil); err != nil {
		t.Fatalf("process: %v", err)
	}

	users := 0
	sawChapterCtx := false
	for _, m := range msgs() {
		if strings.HasPrefix(m, "SYSTEM:") {
			if strings.Contains(m, "[当前章节]") {
				sawChapterCtx = true
			}
			continue
		}
		users++
		if strings.Contains(m, "First paragraph of chapter one.") &&
			strings.Contains(m, "Second paragraph of chapter one.") {
			// expected: both paragraphs of ch1 in one request
		}
	}
	if users != 2 {
		t.Fatalf("chapter mode must batch ch1's two paragraphs (2 requests total), got %d", users)
	}
	if !sawChapterCtx {
		t.Errorf("chapter mode prompt missing [当前章节] context")
	}
}

// TestChapterMode_Off_ResumesLegacyState verifies a classic-mode run hits
// legacy-format checkpoints: with a full legacy state map, no request is
// sent to the model at all.
func TestChapterMode_Off_ResumesLegacyState(t *testing.T) {
	server, msgs := chapterToggleServer(t)
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "m",
		MaxChunkSize: 2400, Concurrency: 1, RequestTimeoutSec: 5, MaxRetries: 1,
	}
	proc := processor.New(cfg, translator.New(cfg))

	state := map[string]string{
		"c1_1-0": "译一", "c1_2-0": "译二", "c2_1-0": "译三",
	}
	out, _, err := proc.Process(context.Background(), chapterBlocks(), state, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := len(msgs()); got != 0 {
		t.Fatalf("legacy state must satisfy classic mode without model calls, got %d requests", got)
	}
	joined := out[0].TranslatedText + out[1].TranslatedText + out[2].TranslatedText
	for _, want := range []string{"译一", "译二", "译三"} {
		if !strings.Contains(joined, want) {
			t.Errorf("legacy translation %q missing from output", want)
		}
	}
}

// TestChapterMode_ConfigDefaults verifies chapter batching defaults to off
// when the JSON config omits the field (the web UI default).
func TestChapterMode_ConfigDefaults(t *testing.T) {
	var cfg config.Config
	if err := json.Unmarshal([]byte(`{"api_url":"http://x","model":"m","prompt":"p"}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ChapterBatching {
		t.Errorf("chapter batching must default to off")
	}
	if got := config.ModeLabel(&cfg); got != "逐段直译" {
		t.Errorf("mode label = %q, want 逐段直译", got)
	}
	cfg.ChapterBatching = true
	if got := config.ModeLabel(&cfg); got != "章节批处理" {
		t.Errorf("mode label = %q, want 章节批处理", got)
	}
}
