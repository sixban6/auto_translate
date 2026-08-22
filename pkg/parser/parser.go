package parser

import (
	"fmt"
	"strings"
)

// TextBlock represents a chunk of text extracted from the document.
type TextBlock struct {
	ID           string
	OriginalText string
	// ChapterID groups blocks that belong to the same chapter. Blocks in the
	// same chapter share translation context. Empty means "whole document".
	ChapterID string
	// ChapterTitle is a short human readable title used as translation
	// context (e.g. "Chapter 3 - The Rally"). May be empty.
	ChapterTitle string
	// HeadingLevel is 1-6 when the block is a heading element (h1..h6),
	// 0 otherwise. Processors use it to cut real chapters inside large
	// chapter files or split-file streams.
	HeadingLevel int
}

// TranslatedBlock represents the translation result for a corresponding TextBlock.
type TranslatedBlock struct {
	ID             string
	TranslatedText string
}

// normalizedText collapses all whitespace for equality checks. Used by the
// bilingual assembly guards: an echo whose line wrapping or spacing was
// reflowed by the model must still be recognized as untranslated, otherwise
// the source paragraph would be duplicated in the output.
func normalizedText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// Parser defines the interface for different file format handlers.
// The parser may be stateful per file.
type Parser interface {
	// Extract extracts text from the input file.
	Extract(inputPath string) ([]TextBlock, error)
	// Assemble reconstructs the document using the translated blocks.
	Assemble(blocks []TranslatedBlock, outputPath string, bilingual bool) error
}

// GetParser returns the appropriate parser based on the file extension.
func GetParser(ext string) (Parser, error) {
	ext = strings.ToLower(ext)
	if ext == ".txt" {
		return NewTxtParser(), nil
	} else if ext == ".epub" {
		return NewEpubParser(), nil
	}
	return nil, fmt.Errorf("unsupported extension: %s", ext)
}
