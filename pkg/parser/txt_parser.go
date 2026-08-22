package parser

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// TxtParser handles basic plain text files.
type TxtParser struct {
	originalParagraphs []string // cached paragraphs to reconstruct the file
}

// NewTxtParser creates a new TXT parser.
func NewTxtParser() *TxtParser {
	return &TxtParser{}
}

var txtChapterHeadingPattern = regexp.MustCompile(
	`^(?i:chapter\s+(?:[0-9]+|[ivxlcdm]+|[a-z])|第\s*[0-9一二三四五六七八九十百千万零两]+\s*[章节回卷部篇]|序章|序言|前言|楔子|引子|尾声|后记|终章|prologue|epilogue)\b?[:：.、\s]?[^\n]{0,60}$`,
)

// isChapterHeading reports whether a paragraph looks like a chapter heading
// (e.g. "Chapter 12", "第十三章 大厦将倾", "PROLOGUE").
func isChapterHeading(para string) bool {
	trimmed := strings.TrimSpace(para)
	if trimmed == "" {
		return false
	}
	runeCount := 0
	for range trimmed {
		runeCount++
	}
	if runeCount > 80 {
		return false
	}
	if !txtChapterHeadingPattern.MatchString(trimmed) {
		return false
	}
	// Heading-like paragraphs should not read like full sentences.
	if strings.Count(trimmed, ".") > 2 || strings.Count(trimmed, "，") > 2 || strings.Count(trimmed, ",") > 2 {
		return false
	}
	return true
}

func hasLetterOrHan(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// Extract splits the standard text file into paragraph blocks, grouping them
// into chapters when chapter headings are detected.
func (p *TxtParser) Extract(inputPath string) ([]TextBlock, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}

	// Normalize CRLF to LF
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	p.originalParagraphs = strings.Split(text, "\n\n")

	var blocks []TextBlock
	chapterCounter := 0
	chapterID := ""
	chapterTitle := ""
	for i, para := range p.originalParagraphs {
		if strings.TrimSpace(para) == "" {
			continue // skip completely empty paragraphs
		}
		if isChapterHeading(para) {
			chapterID = fmt.Sprintf("txt_ch_%d", chapterCounter)
			chapterCounter++
			chapterTitle = strings.TrimSpace(para)
		} else if chapterID == "" {
			// Content before the first heading: its own implicit chapter.
			chapterID = fmt.Sprintf("txt_ch_%d", chapterCounter)
			chapterCounter++
			chapterTitle = ""
		}
		blocks = append(blocks, TextBlock{
			ID:           fmt.Sprintf("txt_%d", i),
			OriginalText: strings.TrimSpace(para),
			ChapterID:    chapterID,
			ChapterTitle: chapterTitle,
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
			// An untranslated paragraph must not be duplicated: only append
			// the translation when it actually differs from the source (after
			// whitespace normalization, so reflowed echoes are caught too).
			if normalizedText(translated) != normalizedText(para) {
				sb.WriteString(fmt.Sprintf("%s\n%s\n\n", para, translated))
			} else {
				sb.WriteString(fmt.Sprintf("%s\n\n", para))
			}
		} else {
			sb.WriteString(fmt.Sprintf("%s\n\n", translated))
		}
	}

	// Trim last double newline
	output := strings.TrimSuffix(sb.String(), "\n\n")

	return os.WriteFile(outputPath, []byte(output), 0644)
}
