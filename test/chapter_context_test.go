package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

func TestTxtParserChapterDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.txt")
	content := "Intro paragraph before any heading.\n\n" +
		"第一章 大厦将倾\n\n" +
		"Content of chapter one.\n\n" +
		"Chapter 2: The Rally\n\n" +
		"Content of chapter two.\n\nMore of chapter two."
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	p := parser.NewTxtParser()
	blocks, err := p.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(blocks) != 6 {
		t.Fatalf("Expected 6 blocks, got %d", len(blocks))
	}

	// Pre-heading content forms its own implicit chapter.
	if blocks[0].ChapterID != "txt_ch_0" {
		t.Errorf("block 0 chapter = %q", blocks[0].ChapterID)
	}
	if blocks[1].ChapterID != "txt_ch_1" || blocks[1].ChapterTitle != "第一章 大厦将倾" {
		t.Errorf("chapter 1 id/title = %q/%q", blocks[1].ChapterID, blocks[1].ChapterTitle)
	}
	if blocks[2].ChapterID != "txt_ch_1" {
		t.Errorf("chapter 1 content chapter = %q", blocks[2].ChapterID)
	}
	if blocks[3].ChapterID != "txt_ch_2" || blocks[3].ChapterTitle != "Chapter 2: The Rally" {
		t.Errorf("chapter 2 id/title = %q/%q", blocks[3].ChapterID, blocks[3].ChapterTitle)
	}
	if blocks[5].ChapterID != "txt_ch_2" {
		t.Errorf("chapter 2 tail chapter = %q", blocks[5].ChapterID)
	}
}

// captureServer records every request's system+user content and replies with a
// paragraph-preserving echo of the user message.
type capturedRequest struct {
	Path   string
	System string
	User   string
}

func newCaptureServer(t *testing.T, replyFn func(user string) string) (*httptest.Server, *[]capturedRequest, *sync.Mutex) {
	var mu sync.Mutex
	var reqs []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var sys, user string
		for _, m := range payload.Messages {
			if m.Role == "system" {
				sys = m.Content
			}
			if m.Role == "user" {
				user = m.Content
			}
		}
		mu.Lock()
		reqs = append(reqs, capturedRequest{Path: r.URL.Path, System: sys, User: user})
		mu.Unlock()

		resp := replyFn(user)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": resp}},
			},
		})
	}))
	return server, &reqs, &mu
}

func paragraphEcho(prefix, user string) string {
	paras := strings.Split(user, "\n\n")
	for i := range paras {
		paras[i] = prefix + paras[i]
	}
	return strings.Join(paras, "\n\n")
}

func TestProcessorChapterContextBatches(t *testing.T) {
	server, reqsPtr, mu := newCaptureServer(t, func(user string) string {
		if strings.Contains(user, "alpha-one") {
			// Unique tail so we can verify rolling context in the next batch.
			return paragraphEcho("译", user) + "\n\nTAILSENTINEL"
		}
		return paragraphEcho("译", user)
	})
	defer server.Close()

	cfg := &config.Config{
		MaxChunkSize:      130,
		APIURL:            server.URL,
		Model:             "translategemma:12b",
		RequestTimeoutSec: 10,
		MaxRetries:        1,
		Concurrency:       1,
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	chOne := []parser.TextBlock{
		{ID: "ch1_a", OriginalText: "alpha-one paragraph with enough body text.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
		{ID: "ch1_b", OriginalText: "alpha-two paragraph with enough body text.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
		{ID: "ch1_c", OriginalText: "alpha-three paragraph with plenty of body.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
		{ID: "ch1_d", OriginalText: "alpha-four paragraph with plenty of body.", ChapterID: "ch1", ChapterTitle: "Chapter One"},
	}
	chTwo := []parser.TextBlock{
		{ID: "ch2_a", OriginalText: "beta-one paragraph from the second chapter.", ChapterID: "ch2", ChapterTitle: "Chapter Two"},
		{ID: "ch2_b", OriginalText: "beta-two paragraph from the second chapter.", ChapterID: "ch2", ChapterTitle: "Chapter Two"},
	}
	blocks := append(append([]parser.TextBlock{}, chOne...), chTwo...)

	translated, stats, err := proc.Process(context.Background(), blocks, nil, nil, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if stats.SuccessCount == 0 {
		t.Fatalf("no successful batches: %+v", stats)
	}

	mu.Lock()
	reqs := append([]capturedRequest{}, (*reqsPtr)...)
	mu.Unlock()

	// 4 chapter-one blocks of ~46 runes pair up into 2 batches (cap 130),
	// chapter two's 2 blocks form 1 batch: 3 requests total.
	if len(reqs) != 3 {
		t.Fatalf("Expected 3 batch requests, got %d: %+v", len(reqs), reqs)
	}

	// No batch may mix paragraphs from different chapters.
	for _, r := range reqs {
		hasAlpha := strings.Contains(r.User, "alpha-")
		hasBeta := strings.Contains(r.User, "beta-")
		if hasAlpha && hasBeta {
			t.Fatalf("batch mixes chapters: %q", r.User)
		}
	}

	// Chapter title is injected as context.
	if !strings.Contains(reqs[0].System, "Chapter One") {
		t.Errorf("first batch missing chapter title context: %q", reqs[0].System)
	}
	if !strings.Contains(reqs[2].System, "Chapter Two") {
		t.Errorf("chapter two batch missing title context: %q", reqs[2].System)
	}

	// Rolling context: second batch of chapter one carries the tail of the
	// first batch's translation, but chapter two's batch does not.
	if !strings.Contains(reqs[1].System, "TAILSENTINEL") {
		t.Errorf("rolling context missing in second batch of chapter one: %q", reqs[1].System)
	}
	if strings.Contains(reqs[2].System, "TAILSENTINEL") {
		t.Errorf("rolling context leaked across chapters: %q", reqs[2].System)
	}

	// Paragraph structure preserved per block.
	transMap := map[string]string{}
	for _, b := range translated {
		transMap[b.ID] = b.TranslatedText
	}
	if transMap["ch1_a"] != "译alpha-one paragraph with enough body text." {
		t.Errorf("ch1_a = %q", transMap["ch1_a"])
	}
	if transMap["ch2_b"] != "译beta-two paragraph from the second chapter." {
		t.Errorf("ch2_b = %q", transMap["ch2_b"])
	}
}

func TestProcessorNewFormatResume(t *testing.T) {
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
	}
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "First paragraph of the document."},
		{ID: "2", OriginalText: "Second paragraph of the document."},
		{ID: "3", OriginalText: "Third paragraph, another chapter.", ChapterID: "c2", ChapterTitle: "C2"},
	}

	// Both chapter-one paragraphs share batch doc@0; block 3 is batch c2@0.
	stateMap := map[string]string{
		"doc@0": "_cached_one_\n\n_cached_two_",
	}
	translated, _, err := proc.Process(context.Background(), blocks, stateMap, nil, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	mu.Lock()
	n := len(*reqsPtr)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("Expected only 1 request (doc@0 served from state), got %d", n)
	}

	transMap := map[string]string{}
	for _, b := range translated {
		transMap[b.ID] = b.TranslatedText
	}
	if transMap["1"] != "_cached_one_" || transMap["2"] != "_cached_two_" {
		t.Errorf("cached batch not mapped back to blocks: %q / %q", transMap["1"], transMap["2"])
	}
	if transMap["3"] != "译Third paragraph, another chapter." {
		t.Errorf("block 3 = %q", transMap["3"])
	}
}

func TestProcessorReassembleLegacyAndNewKeys(t *testing.T) {
	cfg := &config.Config{MaxChunkSize: 1000, Concurrency: 1}
	proc := processor.New(cfg, nil)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "First paragraph."},
		{ID: "2", OriginalText: "Second paragraph."},
		{ID: "3", OriginalText: "Third paragraph.", ChapterID: "c2"},
	}

	stateMap := map[string]string{
		"doc@0": "新一批译文甲\n\n新一批译文乙", // covers blocks 1+2 (new key)
		"3-0":   "旧版块级译文",           // legacy key for block 3
	}
	out := proc.Reassemble(blocks, stateMap)
	got := map[string]string{}
	for _, b := range out {
		got[b.ID] = b.TranslatedText
	}
	if got["1"] != "新一批译文甲" || got["2"] != "新一批译文乙" {
		t.Errorf("new-format batch keys not honored: %q / %q", got["1"], got["2"])
	}
	if got["3"] != "旧版块级译文" {
		t.Errorf("legacy block key not honored: %q", got["3"])
	}

	// Missing coverage falls back to original text.
	empty := proc.Reassemble(blocks, nil)
	for i, b := range empty {
		if b.TranslatedText != blocks[i].OriginalText {
			t.Errorf("expected original fallback, got %q", b.TranslatedText)
		}
	}
}

func TestTranslatorEnginePayloads(t *testing.T) {
	t.Run("mlx posts openai payload as-is", func(t *testing.T) {
		var gotPath string
		var payload map[string]interface{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			json.NewDecoder(r.Body).Decode(&payload)
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}))
		defer server.Close()

		cfg := &config.Config{
			Engine:           "mlx",
			APIURL:           server.URL,
			Model:            "mlx-community/Qwen2.5-7B-Instruct-4bit",
			Prompt:           "p",
			RequestTimeoutSec: 5,
			MaxRetries:       1,
		}
		tr := translator.New(cfg)
		out, status, err := tr.Translate(context.Background(), "hello world text")
		if err != nil || status != translator.StatusSuccess || out != "ok" {
			t.Fatalf("translate: %q %v %v", out, status, err)
		}
		if gotPath != "/" {
			t.Errorf("mlx should post to api_url as-is, got path %q", gotPath)
		}
		if _, hasThink := payload["think"]; hasThink {
			t.Errorf("mlx payload must not carry ollama 'think' field")
		}
		if _, hasOpts := payload["options"]; hasOpts {
			t.Errorf("mlx payload must not carry ollama 'options' field")
		}
		if mt, ok := payload["max_tokens"].(float64); !ok || mt < 1024 {
			t.Errorf("mlx payload missing sane max_tokens: %#v", payload["max_tokens"])
		}
	})

	t.Run("ollama converts to api/chat", func(t *testing.T) {
		var gotPath string
		var payload map[string]interface{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			json.NewDecoder(r.Body).Decode(&payload)
			w.Write([]byte(`{"message":{"content":"ok"}}`))
		}))
		defer server.Close()

		cfg := &config.Config{
			Engine:           "ollama",
			APIURL:           server.URL + "/v1/chat/completions",
			Model:            "qwen2.5:14b",
			Prompt:           "p",
			RequestTimeoutSec: 5,
			MaxRetries:       1,
		}
		tr := translator.New(cfg)
		out, _, err := tr.Translate(context.Background(), "hello world text")
		if err != nil || out != "ok" {
			t.Fatalf("translate: %q %v", out, err)
		}
		if gotPath != "/api/chat" {
			t.Errorf("ollama should post to /api/chat, got %q", gotPath)
		}
		if think, ok := payload["think"].(bool); !ok || think {
			t.Errorf("ollama payload think expected false: %#v", payload["think"])
		}
		opts, ok := payload["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("ollama payload missing options: %#v", payload["options"])
		}
		if _, ok := opts["num_ctx"]; !ok {
			t.Errorf("ollama options missing num_ctx: %#v", opts)
		}
	})
}

func TestResolveEngine(t *testing.T) {
	cases := []struct {
		engine, apiURL, model, want string
	}{
		{"", "", "", translator.EngineOmlx},                                              // default engine
		{"omlx", "http://127.0.0.1:8080/v1/chat/completions", "x", translator.EngineOmlx},  // explicit wins
		{"", "http://127.0.0.1:8000/v1/chat/completions", "Qwen3.8-27B-oQ4e-mtp", translator.EngineOmlx}, // :8000 hint
		{"mlx", "http://localhost:11434/v1/chat/completions", "x", translator.EngineMLX},  // explicit wins
		{"ollama", "http://127.0.0.1:8080/v1/chat/completions", "mlx-community/Qwen2.5-7B", translator.EngineOllama},
		{"", "http://localhost:11434/v1/chat/completions", "qwen", translator.EngineOllama}, // URL hint
		{"", "http://127.0.0.1:8080/v1/chat/completions", "mlx-community/Qwen2.5-7B-Instruct-4bit", translator.EngineMLX},
		{"", "http://127.0.0.1:8080", "qwen2.5:14b", translator.EngineOllama},              // naming style
		{"", "", "translategemma:12b", translator.EngineMLX},                              // legacy special case
	}
	for i, c := range cases {
		if got := config.ResolveEngine(c.apiURL, c.model, c.engine); got != c.want {
			t.Errorf("case %d: ResolveEngine(%q,%q,%q) = %q, want %q", i, c.apiURL, c.model, c.engine, got, c.want)
		}
	}
}

func TestProcessorProportionalMappingFallback(t *testing.T) {
	// Model merges everything into one paragraph: mapping falls back to a
	// proportional split so every block still receives translated text.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "第一段很长很长的译文内容。第二段也很长，包含更多文字。第三段收尾。"}},
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
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: strings.Repeat("甲", 30)},
		{ID: "2", OriginalText: strings.Repeat("乙", 30)},
		{ID: "3", OriginalText: strings.Repeat("丙", 30)},
	}
	translated, _, err := proc.Process(context.Background(), blocks, nil, nil, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	var total int
	for _, b := range translated {
		if strings.TrimSpace(b.TranslatedText) == "" {
			t.Errorf("block %s got empty translation", b.ID)
		}
		total += len([]rune(b.TranslatedText))
	}
	expected := len([]rune("第一段很长很长的译文内容。第二段也很长，包含更多文字。第三段收尾。"))
	if total < expected-6 { // allow trimmed punctuation
		t.Errorf("proportional split lost text: total %d < %d", total, expected)
	}
	_ = fmt.Sprint()
}

// TestOmlxEndpointPathCompletion verifies that oMLX URLs given in the
// conventional base form (http://127.0.0.1:8000/v1) or a bare host are
// completed to the full chat completions path before the request is sent.
func TestOmlxEndpointPathCompletion(t *testing.T) {
	cases := []struct {
		name, urlShape, wantPath string
	}{
		{"base v1 form", "http://%s/v1", "/v1/chat/completions"},
		{"full endpoint", "http://%s/v1/chat/completions", "/v1/chat/completions"},
		{"bare host", "http://%s", "/v1/chat/completions"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()
			go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))

			hostPort := net.JoinHostPort("127.0.0.1", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
			cfg := &config.Config{
				Engine:           "omlx",
				APIURL:           fmt.Sprintf(tc.urlShape, hostPort),
				Model:            "Qwen3.8-27B-oQ4e-mtp",
				Prompt:           "p",
				RequestTimeoutSec: 5,
				MaxRetries:       1,
			}
			tr := translator.New(cfg)
			if _, _, err := tr.Translate(context.Background(), "hello translation text"); err != nil {
				t.Fatalf("translate: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("url %q resolved to path %q, want %q", cfg.APIURL, gotPath, tc.wantPath)
			}
		})
	}
}
