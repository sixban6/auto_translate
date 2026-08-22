package test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// TestProcess_RetryRecoversUntranslatedParagraphs reproduces the "paragraph
// left completely untranslated" bug: a small model echoes multi-paragraph
// batches verbatim. The second-chance pass must re-send each paragraph as a
// single-paragraph request and recover the batch.
func TestProcess_RetryRecoversUntranslatedParagraphs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)

		var text string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				text = m.Content
				break
			}
		}

		content := "[T]" + text
		if strings.Contains(text, "\n\n") {
			// Multi-paragraph batch: echo the source back untranslated.
			content = text
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": content}},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL:            server.URL,
		Model:             "dummy",
		MaxChunkSize:      2000,
		Concurrency:       1,
		MaxRetries:        1,
		RequestTimeoutSec: 10,
	}
	proc := processor.New(cfg, translator.New(cfg))

	blocks := []parser.TextBlock{
		{ID: "1", ChapterID: "c1", OriginalText: "Alpha paragraph with enough length one."},
		{ID: "2", ChapterID: "c1", OriginalText: "Beta paragraph with enough length two."},
	}

	var msgs []string
	translated, stats, err := proc.Process(context.Background(), blocks, nil, func(c, total int, msg string) {
		if msg != "" {
			msgs = append(msgs, msg)
		}
	}, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	for i, want := range []string{"[T]Alpha paragraph with enough length one.", "[T]Beta paragraph with enough length two."} {
		if translated[i].TranslatedText != want {
			t.Errorf("block %d not translated by retry pass: %q", i, translated[i].TranslatedText)
		}
	}
	if stats.FallbackCount != 0 || stats.FailureCount != 0 || stats.RefusedCount != 0 {
		t.Errorf("recovered batch must not stay degraded: %+v", stats)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("expected recovered batch counted as success, got %+v", stats)
	}
	sawRetry := false
	for _, m := range msgs {
		if strings.Contains(m, "🔁") {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Errorf("expected a retry notification, got: %v", msgs)
	}
}

// TestTxtAssemble_BilingualDoesNotDuplicateUntranslated ensures that when a
// paragraph was not translated (translation equals the original), bilingual
// output keeps it once instead of printing the same text twice.
func TestTxtAssemble_BilingualDoesNotDuplicateUntranslated(t *testing.T) {
	dir := t.TempDir()
	inFile := filepath.Join(dir, "book.txt")
	outFile := filepath.Join(dir, "book_out.txt")
	os.WriteFile(inFile, []byte("First paragraph here.\n\nSecond paragraph here.\n"), 0644)

	p := parser.NewTxtParser()
	blocks, err := p.Extract(inFile)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	tBlocks := []parser.TranslatedBlock{
		{ID: blocks[0].ID, TranslatedText: "First paragraph here."}, // untranslated echo
		{ID: blocks[1].ID, TranslatedText: "第二段内容。"},
	}
	if err := p.Assemble(tBlocks, outFile, true); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	out, _ := os.ReadFile(outFile)
	content := string(out)

	if got := strings.Count(content, "First paragraph here."); got != 1 {
		t.Errorf("untranslated paragraph must appear exactly once in bilingual output, got %d times: %q", got, content)
	}
	if got := strings.Count(content, "Second paragraph here."); got != 1 {
		t.Errorf("translated paragraph original must appear exactly once, got %d times: %q", got, content)
	}
	if !strings.Contains(content, "第二段内容。") {
		t.Errorf("real translation must still be injected bilingually: %q", content)
	}
}

// TestEpubAssemble_BilingualDoesNotDuplicateUntranslated is the epub variant
// of the no-duplicate guarantee.
func TestEpubAssemble_BilingualDoesNotDuplicateUntranslated(t *testing.T) {
	dir := t.TempDir()
	testZip := filepath.Join(dir, "test.epub")
	outFile := filepath.Join(dir, "test_out.epub")

	f, _ := os.Create(testZip)
	w := zip.NewWriter(f)
	m, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	m.Write([]byte("application/epub+zip"))
	h, _ := w.Create("OEBPS/test.xhtml")
	h.Write([]byte("<html><body><p>Hello World</p></body></html>"))
	w.Close()
	f.Close()

	p := parser.NewEpubParser()
	blocks, err := p.Extract(testZip)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	tBlocks := []parser.TranslatedBlock{
		{ID: blocks[0].ID, TranslatedText: "Hello World"}, // untranslated echo
	}
	if err := p.Assemble(tBlocks, outFile, true); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	r, err := zip.OpenReader(outFile)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer r.Close()
	var newHtml string
	for _, zf := range r.File {
		if zf.Name == "OEBPS/test.xhtml" {
			rc, _ := zf.Open()
			buf, _ := io.ReadAll(rc)
			newHtml = string(buf)
			rc.Close()
		}
	}
	if got := strings.Count(newHtml, "Hello World"); got != 1 {
		t.Errorf("untranslated paragraph must appear exactly once in bilingual epub, got %d times: %s", got, newHtml)
	}
}
