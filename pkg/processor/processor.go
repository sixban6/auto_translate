package processor

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/translator"
)

type Processor struct {
	cfg        *config.Config
	translator *translator.Translator
}

func New(cfg *config.Config, tr *translator.Translator) *Processor {
	return &Processor{
		cfg:        cfg,
		translator: tr,
	}
}

// SubChunk represents a broken down piece of a TextBlock
type SubChunk struct {
	BlockID    string
	SubIndex   int
	Text       string
	Translated string
	Err        error
}

// getFilePrefix extracts the file path part from an ID like "OEBPS/ch1.xhtml_node_5"
func getFilePrefix(id string) string {
	idx := strings.LastIndex(id, "_node_")
	if idx > 0 {
		return id[:idx]
	}
	return ""
}

// Process handles chunking, concurrent translation, and reassembly.
func (p *Processor) Process(blocks []parser.TextBlock, onProgress func(int, int, string)) ([]parser.TranslatedBlock, error) {
	// 0. Pre-processing (Context Aggregation for short texts)
	var mergedBlocks []parser.TextBlock
	skipMap := make(map[string]bool)

	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		runes := []rune(b.OriginalText)

		// Trigger condition: short text block (length < 30)
		if len(runes) < 30 {
			prefix := getFilePrefix(b.ID)
			mergedText := b.OriginalText

			j := i + 1
			for ; j < len(blocks); j++ {
				nextB := blocks[j]
				if prefix != "" && getFilePrefix(nextB.ID) != prefix {
					break // strictly same file
				}
				mergedText += " " + nextB.OriginalText
				skipMap[nextB.ID] = true

				// Stop merging if we've accumulated enough context
				if len([]rune(mergedText)) >= 60 {
					j++
					break
				}
			}
			b.OriginalText = mergedText
			i = j - 1
		}
		mergedBlocks = append(mergedBlocks, b)
	}

	var subChunks []SubChunk

	// 1. Chunking
	for _, b := range mergedBlocks {
		chunks := p.splitText(b.OriginalText)
		for i, cText := range chunks {
			if strings.TrimSpace(cText) == "" {
				continue
			}
			subChunks = append(subChunks, SubChunk{
				BlockID:  b.ID,
				SubIndex: i,
				Text:     cText,
			})
		}
	}

	totalJobs := len(subChunks)
	if onProgress != nil {
		onProgress(0, totalJobs, "")
	}

	// 2. Concurrency Control (Worker Pool)
	jobs := make(chan int, totalJobs)
	results := make(chan int, totalJobs)
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < p.cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				translated, err := p.translator.Translate(subChunks[j].Text, func(msg string) {
					if onProgress != nil {
						onProgress(-1, totalJobs, fmt.Sprintf("[块 %s-%d] %s", subChunks[j].BlockID, subChunks[j].SubIndex, msg))
					}
				})
				subChunks[j].Translated = translated
				subChunks[j].Err = err
				results <- j
			}
		}()
	}

	// Dispatch jobs
	for j := range subChunks {
		jobs <- j
	}
	close(jobs)

	// Wait in a separate goroutine so we can close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect
	completedCount := 0
	for j := range results {
		completedCount++
		if onProgress != nil {
			var msg string
			if subChunks[j].Err == nil {
				preview := strings.ReplaceAll(subChunks[j].Translated, "\n", " ")
				runes := []rune(preview)
				if len(runes) > 60 {
					preview = string(runes[:57]) + "..."
				}
				msg = fmt.Sprintf("✅ [分块 %s-%d] 完成: %s", subChunks[j].BlockID, subChunks[j].SubIndex, preview)
			} else {
				msg = fmt.Sprintf("❌ [分块 %s-%d] 翻译完全失败，准备降级容错。", subChunks[j].BlockID, subChunks[j].SubIndex)
			}
			onProgress(completedCount, totalJobs, msg)
		}
	}

	// Fallback to original text on errors instead of failing globally
	errorCount := 0
	for i, sc := range subChunks {
		if sc.Err != nil {
			errorCount++
			subChunks[i].Translated = sc.Text // Fallback to original text
		}
	}

	if errorCount > 0 && onProgress != nil {
		onProgress(-1, totalJobs, fmt.Sprintf("⚠️ 警告: %d 个文本块翻译失败，已降级为原文保留", errorCount))
	}

	// 3. Reassembly
	blocksMap := make(map[string][]SubChunk)
	for _, sc := range subChunks {
		blocksMap[sc.BlockID] = append(blocksMap[sc.BlockID], sc)
	}

	var translatedBlocks []parser.TranslatedBlock
	for _, b := range blocks { // iterate over original unmodified blocks to map all IDs
		if skipMap[b.ID] {
			translatedBlocks = append(translatedBlocks, parser.TranslatedBlock{
				ID:             b.ID,
				TranslatedText: "<!--merged-->", // Special token to prevent parser.Assemble from skipping empty overrides
			})
			continue
		}

		chunks := blocksMap[b.ID]
		// The chunks are already appended in order, but we can trust the stable order from subChunks loop
		// Actually, map iteration is random, but we append from subChunks array which is order-preserving?
		// Wait, blocksMap[b.ID] appends in order because subChunks was created in order and the loop above is over `subChunks` sequentially.
		var sb strings.Builder
		for _, c := range chunks {
			sb.WriteString(c.Translated)
			// For txt, sentences usually need space connection?
			// If we split by ". ", we might have lost the period or kept it.
			// Let's assume Chinese doesn't need much space. We can append it.
		}
		translatedBlocks = append(translatedBlocks, parser.TranslatedBlock{
			ID:             b.ID,
			TranslatedText: sb.String(),
		})
	}

	return translatedBlocks, nil
}

func (p *Processor) splitText(text string) []string {
	if utf8.RuneCountInString(text) <= p.cfg.MaxChunkSize {
		return []string{text}
	}

	// Try splitting by sentences (. )
	var result []string
	sentences := strings.Split(text, ". ")
	var currentChunk strings.Builder

	for i, s := range sentences {
		part := s
		if i < len(sentences)-1 {
			part += ". " // restore the delimiter
		}

		if utf8.RuneCountInString(currentChunk.String())+utf8.RuneCountInString(part) > p.cfg.MaxChunkSize {
			if currentChunk.Len() > 0 {
				result = append(result, currentChunk.String())
				currentChunk.Reset()
			}
			// If 'part' itself is too large, we must hard split it
			if utf8.RuneCountInString(part) > p.cfg.MaxChunkSize {
				runes := []rune(part)
				for len(runes) > 0 {
					cut := p.cfg.MaxChunkSize
					if cut > len(runes) {
						cut = len(runes)
					}
					result = append(result, string(runes[:cut]))
					runes = runes[cut:]
				}
			} else {
				currentChunk.WriteString(part)
			}
		} else {
			currentChunk.WriteString(part)
		}
	}

	if currentChunk.Len() > 0 {
		result = append(result, currentChunk.String())
	}

	return result
}
