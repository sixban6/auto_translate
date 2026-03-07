package parser

import (
	"fmt"
	"os"
	"strings"
)

// TxtParser handles basic plain text files.
type TxtParser struct {
	originalParagraphs []string // cached paragraphs to reconstruct the file
}

// NewTxtParser creates a new TXT parser.
func NewTxtParser() *TxtParser {
	return &TxtParser{}
}

// Extract splits the standard text file into paragraph blocks.
func (p *TxtParser) Extract(inputPath string) ([]TextBlock, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}

	// Normalize CRLF to LF
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	p.originalParagraphs = strings.Split(text, "\n\n")

	var blocks []TextBlock
	for i, para := range p.originalParagraphs {
		if strings.TrimSpace(para) == "" {
			continue // skip completely empty paragraphs
		}
		blocks = append(blocks, TextBlock{
			ID:           fmt.Sprintf("txt_%d", i),
			OriginalText: strings.TrimSpace(para),
		})
	}
	return blocks, nil
}

// Assemble reconstructs the text document, optionally in bilingual format.
func (p *TxtParser) Assemble(blocks []TranslatedBlock, outputPath string, bilingual bool) error {
	if p.originalParagraphs == nil {
		return fmt.Errorf("Extract() must be called before Assemble()")
	}

	// Map ID to TranslatedText
	transMap := make(map[string]string)
	for _, b := range blocks {
		transMap[b.ID] = b.TranslatedText
	}

	var sb strings.Builder
	for i, para := range p.originalParagraphs {
		if strings.TrimSpace(para) == "" {
			sb.WriteString(para + "\n\n")
			continue
		}

		id := fmt.Sprintf("txt_%d", i)
		translated, ok := transMap[id]
		if !ok || strings.TrimSpace(translated) == "" {
			// If not translated for some reason, keep original
			sb.WriteString(para + "\n\n")
			continue
		}

		if bilingual {
			// Output Chinese then English paragraph
			sb.WriteString(fmt.Sprintf("%s\n%s\n\n", translated, para))
		} else {
			sb.WriteString(fmt.Sprintf("%s\n\n", translated))
		}
	}

	// Trim last double newline
	output := strings.TrimSuffix(sb.String(), "\n\n")

	return os.WriteFile(outputPath, []byte(output), 0644)
}
