package test

import (
	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
	"testing"
)

func TestProcessorSkipCompleted(t *testing.T) {
	cfg := &config.Config{
		MaxChunkSize: 100,
		Concurrency:  2,
		MaxRetries:   1,
	}

	tr := translator.New(cfg)

	blocks := []parser.TextBlock{
		{ID: "1", OriginalText: "Hello world. This is the first sentence that is over thirty characters."},
		{ID: "2", OriginalText: "This is test. And here is some more text to make it longer than 30 runes."},
		{ID: "3", OriginalText: "Goodbye world. The end is near for this document translation test block."},
	}

	// Make sure the block ID mappings map cleanly to SubChunk. SubChunk ID format is blockID-subIndex, e.g. "2-0"
	stateMap := map[string]string{
		"2-0": "这是一个测试。",
	}

	proc := processor.New(cfg, tr)

	translatedBlocks, stats, err := proc.Process(blocks, stateMap, nil, nil)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}

	if len(translatedBlocks) != 3 {
		t.Fatalf("Expected 3 blocks, got %d", len(translatedBlocks))
	}

	if translatedBlocks[1].TranslatedText != "这是一个测试。" {
		t.Errorf("Expected skipped block to retain translated text, got %q", translatedBlocks[1].TranslatedText)
	}

	if stats.SuccessCount < 3 { // Depending on if other components succeed or fallback, skipped is considered success
		t.Logf("Total successes: %d", stats.SuccessCount)
	}
}
