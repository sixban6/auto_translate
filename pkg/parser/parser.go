package parser

import (
	"fmt"
	"strings"
)

// TextBlock represents a chunk of text extracted from the document.
type TextBlock struct {
	ID           string
	OriginalText string
}

// TranslatedBlock represents the translation result for a corresponding TextBlock.
type TranslatedBlock struct {
	ID             string
	TranslatedText string
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
