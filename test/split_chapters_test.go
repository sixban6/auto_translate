package test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// Split-fragment epubs (Calibre style "*_split_NNN.html") must aggregate all
// fragments into one chapter stream so paragraphs keep being batched across
// file boundaries.
func TestEpubSplitFragmentsAggregateIntoOneChapter(t *testing.T) {
	// Two fragment files of the same base share ChapterID.
	blocks := []parser.TextBlock{
		{ID: "book_split_001.html_block_0", OriginalText: "First paragraph of fragment one with some body.", ChapterID: "book", ChapterTitle: ""},
		{ID: "book_split_001.html_block_1", OriginalText: "Second paragraph of fragment one.", ChapterID: "book", ChapterTitle: ""},
		{ID: "book_split_002.html_block_0", OriginalText: "First paragraph of fragment two continues the stream.", ChapterID: "book", ChapterTitle: ""},
		{ID: "book_split_002.html_block_1", OriginalText: "Second paragraph of fragment two.", ChapterID: "book", ChapterTitle: ""},
	}

	server, reqsPtr, mu := newCaptureServer(t, func(user string) string {
		return paragraphEcho("译", user)
	})
	defer server.Close()

	cfg := &config.Config{
		MaxChunkSize:      1000,
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		RequestTimeoutSec: 10,
		MaxRetries:        1,
		Concurrency:       1,
		ChapterBatching:   true,
	}
	proc := processor.New(cfg, translator.New(cfg))
	if _, _, err := proc.Process(context.Background(), blocks, nil, nil, nil); err != nil {
		t.Fatalf("Process: %v", err)
	}

	mu.Lock()
	reqs := append([]capturedRequest{}, (*reqsPtr)...)
	mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("Expected all 4 paragraphs (across both fragments) in ONE batch, got %d requests", len(reqs))
	}
	if strings.Count(reqs[0].User, "\n\n") != 3 {
		t.Errorf("Batch should contain 4 paragraphs, got: %q", reqs[0].User)
	}
}

// h1/h2 heading blocks inside a stream cut real chapters: the heading starts
// a new chapter whose title feeds the context of its batches.
func TestHeadingBlocksCutChapters(t *testing.T) {
	server, reqsPtr, mu := newCaptureServer(t, func(user string) string {
		return paragraphEcho("译", user)
	})
	defer server.Close()

	cfg := &config.Config{
		MaxChunkSize:      1000,
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		RequestTimeoutSec: 10,
		MaxRetries:        1,
		Concurrency:       1,
		ChapterBatching:   true,
	}
	proc := processor.New(cfg, translator.New(cfg))

	blocks := []parser.TextBlock{
		{ID: "s1_b0", OriginalText: "Chapter 1: The Wind", ChapterID: "book", HeadingLevel: 2},
		{ID: "s1_b1", OriginalText: "Body of chapter one, first paragraph.", ChapterID: "book"},
		{ID: "s1_b2", OriginalText: "Body of chapter one, second paragraph.", ChapterID: "book"},
		{ID: "s1_b3", OriginalText: "Chapter 2: The Antidote", ChapterID: "book", HeadingLevel: 2},
		{ID: "s1_b4", OriginalText: "Body of chapter two, first paragraph.", ChapterID: "book"},
	}
	if _, _, err := proc.Process(context.Background(), blocks, nil, nil, nil); err != nil {
		t.Fatalf("Process: %v", err)
	}

	mu.Lock()
	reqs := append([]capturedRequest{}, (*reqsPtr)...)
	mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("Expected 2 chapter batches, got %d", len(reqs))
	}
	if !strings.Contains(reqs[0].User, "Chapter 1") || !strings.Contains(reqs[0].User, "chapter one, first") {
		t.Errorf("first batch should hold chapter 1 content: %q", reqs[0].User)
	}
	if !strings.Contains(reqs[1].System, "Chapter 2: The Antidote") {
		t.Errorf("second batch should carry the chapter 2 title context: %q", reqs[1].System)
	}
	if strings.Contains(reqs[1].System, "前文译文") {
		t.Errorf("rolling context must not cross chapter boundaries: %q", reqs[1].System)
	}
}

// Pausing mid-flight must present as "paused, resumable" instead of a
// translation failure, and must not pollute failure statistics.
func TestPauseMidFlightShowsResumableNotFailure(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the in-flight request until the test cancels
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })

	cfg := &config.Config{
		MaxChunkSize:      1000,
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		RequestTimeoutSec: 10,
		MaxRetries:        3,
		Concurrency:       1,
	}
	proc := processor.New(cfg, translator.New(cfg))
	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "First paragraph that will be in flight when paused."},
		{ID: "2", OriginalText: "Second paragraph never reached."},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var msgs []string
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, stats, err := proc.Process(ctx, blocks, nil, func(current, total int, msg string) {
		if msg != "" {
			msgs = append(msgs, msg)
		}
	}, nil)
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}

	for _, m := range msgs {
		if strings.Contains(m, "翻译完全失败") || strings.Contains(m, "❌") {
			t.Errorf("pause surfaced as a failure: %q", m)
		}
	}
	sawPaused := false
	for _, m := range msgs {
		if strings.Contains(m, "已暂停") {
			sawPaused = true
		}
	}
	if !sawPaused {
		t.Errorf("expected a resumable pause message, got: %v", msgs)
	}
	if stats.FailureCount != 0 || len(stats.FailedBlocks) != 0 {
		t.Errorf("pause must not count as failure: %+v", stats)
	}
}

func TestChapterIDForFileAggregatesSplitFragments(t *testing.T) {
	// Direct check of the epub parser helper behavior via Extract on a
	// synthetic epub is covered above; here we assert the naming rule via a
	// tiny zip built in-memory.
	dir := t.TempDir()
	epubPath := filepath.Join(dir, "book.epub")
	buildSplitEpub(t, epubPath)

	p := parser.NewEpubParser()
	blocks, err := p.Extract(epubPath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatalf("no blocks extracted")
	}
	firstChapter := blocks[0].ChapterID
	for _, b := range blocks {
		if b.ChapterID != firstChapter {
			t.Fatalf("split fragments must share one chapter id, got %q vs %q", b.ChapterID, firstChapter)
		}
	}
	// The h2 heading block must be recognized.
	sawHeading := false
	for _, b := range blocks {
		if b.HeadingLevel == 2 {
			sawHeading = true
		}
	}
	if !sawHeading {
		t.Errorf("expected an h2 heading block to be marked")
	}
}

func buildSplitEpub(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	part1 := `<html><body><h2>Chapter One</h2><p>Alpha paragraph one.</p><p>Alpha paragraph two.</p></body></html>`
	part2 := `<html><body><p>Beta paragraph one continues.</p></body></html>`
	for name, content := range map[string]string{
		"book_split_001.html": part1,
		"book_split_002.html": part2,
	} {
		fw, _ := w.Create(name)
		fw.Write([]byte(content))
	}
	w.Close()
}

// A model that echoes the source verbatim is a degradation (⚠️, kept as
// original), never a hard failure (❌).
func TestEchoedBatchShowsWarningNotFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// Echo the input back unchanged (model failed to translate).
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": user}},
			},
		})
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
	proc := processor.New(cfg, translator.New(cfg))
	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "Prologue"},
		{ID: "2", OriginalText: "Some real paragraph content here."},
	}

	var msgs []string
	translated, stats, err := proc.Process(context.Background(), blocks, nil, func(c, t int, msg string) {
		if msg != "" {
			msgs = append(msgs, msg)
		}
	}, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m, "❌") || strings.Contains(m, "翻译完全失败") {
			t.Errorf("echo should not surface as failure: %q", m)
		}
	}
	sawWarning := false
	for _, m := range msgs {
		if strings.Contains(m, "⚠️") && strings.Contains(m, "保留原文") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Errorf("expected a degradation warning, got: %v", msgs)
	}
	if stats.FailureCount != 0 {
		t.Errorf("echo must not count as failure: %+v", stats)
	}
	// Original text preserved for the echoed batch.
	if translated[0].TranslatedText != "Prologue" {
		t.Errorf("echoed batch should keep original: %q", translated[0].TranslatedText)
	}
}
