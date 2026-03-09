package processor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/translator"
)

type Processor struct {
	cfg        *config.Config
	translator *translator.Translator
}

type FailedBlockInfo struct {
	ID           string `json:"id"`
	OriginalText string `json:"original_text"`
	Error        string `json:"error"`
}

type TranslationStats struct {
	SuccessCount  int               `json:"success_count"`
	FallbackCount int               `json:"fallback_count"`
	RefusedCount  int               `json:"refused_count"`
	FailureCount  int               `json:"failure_count"`
	FailedBlocks  []FailedBlockInfo `json:"failed_blocks,omitempty"`
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
	Status     translator.TranslationStatus
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

func isEpubNodeID(id string) bool {
	return strings.Contains(id, "_node_") || strings.Contains(id, "_block_")
}

func isASCIIAlphaDashPiece(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '–' || r == '—' {
			continue
		}
		return false
	}
	return true
}

func hasDashRune(s string) bool {
	return strings.ContainsAny(s, "-–—")
}

func endsWithDash(s string) bool {
	return strings.HasSuffix(s, "-") || strings.HasSuffix(s, "–") || strings.HasSuffix(s, "—")
}

func startsWithDash(s string) bool {
	return strings.HasPrefix(s, "-") || strings.HasPrefix(s, "–") || strings.HasPrefix(s, "—")
}

func shouldMergeEpubHyphenPiece(current, next string) bool {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if !isASCIIAlphaDashPiece(current) || !isASCIIAlphaDashPiece(next) {
		return false
	}
	if utf8.RuneCountInString(current) > 24 || utf8.RuneCountInString(next) > 24 {
		return false
	}
	if !hasDashRune(current) && !hasDashRune(next) {
		return false
	}
	if endsWithDash(current) || startsWithDash(next) {
		return true
	}
	if utf8.RuneCountInString(current) <= 4 || utf8.RuneCountInString(next) <= 4 {
		return true
	}
	return false
}

func isLatinPhrasePiece(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	hasLetter := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			continue
		}
		if (r >= '0' && r <= '9') || r == ' ' || r == '\'' || r == '"' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' {
			continue
		}
		return false
	}
	return hasLetter
}

func startsWithLowerASCIILetter(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r := rune(s[0])
	return r >= 'a' && r <= 'z'
}

func endsWithTerminalPunctuation(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	last := s[len(s)-1]
	return last == '.' || last == '!' || last == '?' || last == ':' || last == ';'
}

func shouldMergeEpubPhrasePiece(current, next string) bool {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if !isLatinPhrasePiece(current) || !isLatinPhrasePiece(next) {
		return false
	}
	if !startsWithLowerASCIILetter(next) {
		return false
	}
	if endsWithTerminalPunctuation(current) {
		return false
	}
	if utf8.RuneCountInString(current)+utf8.RuneCountInString(next) > 100 {
		return false
	}
	return true
}

// Process handles chunking, concurrent translation, and reassembly.
func (p *Processor) Process(ctx context.Context, blocks []parser.TextBlock, stateMap map[string]string, onProgress func(int, int, string), onChunkCompleted func(string, string)) ([]parser.TranslatedBlock, TranslationStats, error) {
	var stats TranslationStats
	if ctx == nil {
		ctx = context.Background()
	}

	// 0. Pre-processing (Context Aggregation for short texts)
	var mergedBlocks []parser.TextBlock
	skipMap := make(map[string]bool)

	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		runes := []rune(b.OriginalText)
		if isEpubNodeID(b.ID) {
			mergedBlocks = append(mergedBlocks, b)
			continue
		}

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
				if ctx.Err() != nil {
					subChunks[j].Err = ctx.Err()
					subChunks[j].Status = translator.StatusFailed
					results <- j
					continue
				}
				translated, status, err := p.translator.Translate(ctx, subChunks[j].Text, func(msg string) {
					if onProgress != nil {
						onProgress(-1, totalJobs, fmt.Sprintf("[块 %s-%d] %s", subChunks[j].BlockID, subChunks[j].SubIndex, msg))
					}
				})
				subChunks[j].Translated = translated
				subChunks[j].Status = status
				subChunks[j].Err = err
				results <- j
			}
		}()
	}

	// Dispatch jobs
	for j := range subChunks {
		if ctx.Err() != nil {
			break
		}
		chunkID := fmt.Sprintf("%s-%d", subChunks[j].BlockID, subChunks[j].SubIndex)
		if stateMap != nil && stateMap[chunkID] != "" {
			subChunks[j].Translated = stateMap[chunkID]
			subChunks[j].Status = translator.StatusSuccess
			results <- j
		} else {
			jobs <- j
		}
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
		sc := subChunks[j]
		if onProgress != nil {
			var msg string
			if sc.Err == nil {
				preview := strings.ReplaceAll(sc.Translated, "\n", " ")
				runes := []rune(preview)
				if len(runes) > 60 {
					preview = string(runes[:57]) + "..."
				}
				msg = fmt.Sprintf("✅ [分块 %s-%d] 完成: %s", sc.BlockID, sc.SubIndex, preview)
			} else {
				msg = fmt.Sprintf("❌ [分块 %s-%d] 翻译完全失败，准备降级容错。", sc.BlockID, sc.SubIndex)
			}
			onProgress(completedCount, totalJobs, msg)
		}

		if sc.Err == nil && sc.Status == translator.StatusSuccess && onChunkCompleted != nil {
			chunkID := fmt.Sprintf("%s-%d", sc.BlockID, sc.SubIndex)
			if stateMap == nil || stateMap[chunkID] == "" {
				onChunkCompleted(chunkID, sc.Translated)
			}
		}

		// Update stats
		switch sc.Status {
		case translator.StatusSuccess:
			stats.SuccessCount++
		case translator.StatusFallback:
			stats.FallbackCount++
		case translator.StatusRefused:
			stats.RefusedCount++
			stats.FailedBlocks = append(stats.FailedBlocks, FailedBlockInfo{
				ID:           fmt.Sprintf("%s-%d", sc.BlockID, sc.SubIndex),
				OriginalText: sc.Text,
				Error:        sc.Err.Error(),
			})
		case translator.StatusFailed:
			stats.FailureCount++
			errText := "unknown error"
			if sc.Err != nil {
				errText = sc.Err.Error()
			}
			stats.FailedBlocks = append(stats.FailedBlocks, FailedBlockInfo{
				ID:           fmt.Sprintf("%s-%d", sc.BlockID, sc.SubIndex),
				OriginalText: sc.Text,
				Error:        errText,
			})
		case translator.StatusSkip:
			// skipped empty chunk, don't count
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
	if ctx.Err() != nil {
		return nil, stats, ctx.Err()
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

	return translatedBlocks, stats, nil
}

func (p *Processor) splitText(text string) []string {
	if utf8.RuneCountInString(text) <= p.cfg.MaxChunkSize {
		return []string{text}
	}

	sentences := splitIntoSentences(text)
	var result []string
	var currentChunk strings.Builder

	flushChunk := func() {
		if currentChunk.Len() > 0 {
			result = append(result, currentChunk.String())
			currentChunk.Reset()
		}
	}

	for _, sentence := range sentences {
		part := strings.TrimSpace(sentence)
		if part == "" {
			continue
		}
		if utf8.RuneCountInString(part) > p.cfg.MaxChunkSize {
			flushChunk()
			subParts := splitByWeakSeparators(part, p.cfg.MaxChunkSize)
			result = append(result, subParts...)
			continue
		}
		if utf8.RuneCountInString(currentChunk.String())+utf8.RuneCountInString(part) > p.cfg.MaxChunkSize {
			flushChunk()
			currentChunk.WriteString(part)
			continue
		}
		if currentChunk.Len() > 0 {
			currentChunk.WriteString(" ")
		}
		currentChunk.WriteString(part)
	}
	flushChunk()
	return result
}

func splitIntoSentences(text string) []string {
	var sentences []string
	var sb strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		sb.WriteRune(r)
		if r == '\n' {
			if strings.TrimSpace(sb.String()) != "" {
				sentences = append(sentences, sb.String())
			}
			sb.Reset()
			continue
		}
		if isSentenceTerminator(r) {
			if r == '.' {
				next := nextNonSpaceRune(runes, i+1)
				if next != 0 && !(next >= 'A' && next <= 'Z') {
					continue
				}
			}
			sentences = append(sentences, sb.String())
			sb.Reset()
		}
	}
	if strings.TrimSpace(sb.String()) != "" {
		sentences = append(sentences, sb.String())
	}
	return sentences
}

func nextNonSpaceRune(runes []rune, start int) rune {
	for i := start; i < len(runes); i++ {
		if !unicode.IsSpace(runes[i]) {
			return runes[i]
		}
	}
	return 0
}

func isSentenceTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	default:
		return false
	}
}

func splitByWeakSeparators(text string, maxLen int) []string {
	var result []string
	var sb strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		sb.WriteRune(r)
		if isWeakSeparator(r) && utf8.RuneCountInString(sb.String()) >= maxLen/2 {
			part := strings.TrimSpace(sb.String())
			if part != "" {
				result = append(result, part)
			}
			sb.Reset()
		}
		if utf8.RuneCountInString(sb.String()) >= maxLen {
			part := strings.TrimSpace(sb.String())
			if part != "" {
				result = append(result, part)
			}
			sb.Reset()
		}
	}
	if strings.TrimSpace(sb.String()) != "" {
		result = append(result, strings.TrimSpace(sb.String()))
	}
	return result
}

func isWeakSeparator(r rune) bool {
	switch r {
	case ',', ';', ':', '，', '；', '：':
		return true
	default:
		return false
	}
}
