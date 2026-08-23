package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

// batchEntry is one paragraph-like unit inside a batch. For oversized blocks
// that had to be split, each piece becomes its own entry with PieceIndex > 0.
// The pair (BlockID, PieceIndex) maps 1:1 onto legacy "{blockID}-{piece}" state
// keys, so old checkpoints still resume.
type batchEntry struct {
	BlockID    string
	PieceIndex int
	Text       string
	Translated string
	Covered    bool

	// SuppressOriginal keeps the entry empty at assembly time. Used when a
	// whole-block legacy translation replaces all pieces of a split block:
	// the remaining pieces must not fall back to their original text or the
	// block would be emitted twice.
	SuppressOriginal bool
}

func (e *batchEntry) legacyKey() string {
	return fmt.Sprintf("%s-%d", e.BlockID, e.PieceIndex)
}

// batch is one translation request: consecutive blocks of the same chapter,
// joined with blank lines so the model keeps paragraph structure.
type batch struct {
	ID      string // stable "{chapter}@{seq}", used as the resume state key
	Chapter string
	Title   string
	Entries []*batchEntry
	Text    string

	Translated string
	Status     translator.TranslationStatus
	Err        error
	FromCache  bool // satisfied from stateMap (new or legacy keys)
}

const docChapterID = "doc"

func joinEntryTexts(entries []*batchEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.Text)
	}
	return strings.Join(parts, "\n\n")
}

// buildBatches groups blocks into chapter-scoped batches deterministically.
// Chapter boundaries are: a change of the parser's ChapterID (per file, with
// converter fragments "*_split_NNN" pre-aggregated) or an h1/h2 heading block
// inside the stream. Blocks are packed while the total stays under
// MaxChunkSize; a block larger than MaxChunkSize is split by sentences, each
// piece becoming its own batch.
func (p *Processor) buildBatches(blocks []parser.TextBlock) []*batch {
	if !p.cfg.ChapterBatching {
		return p.buildClassicBatches(blocks)
	}
	target := p.cfg.MaxChunkSize
	if target <= 0 {
		target = 2400
	}

	var batches []*batch
	var cur *batch
	curFile := "\x00none"    // ChapterID of the current block group
	curChapter := "\x00none" // effective chapter key used in batch IDs
	chapterTitle := ""
	seq := 0
	autoSeq := 0

	flush := func() {
		if cur != nil {
			cur.Text = joinEntryTexts(cur.Entries)
			batches = append(batches, cur)
			cur = nil
		}
	}

	for _, b := range blocks {
		text := strings.TrimSpace(b.OriginalText)
		if text == "" {
			continue
		}
		ch := b.ChapterID
		if ch == "" {
			ch = docChapterID
		}

		if b.HeadingLevel == 1 || b.HeadingLevel == 2 {
			// Real chapter heading inside the stream: start a fresh chapter.
			flush()
			autoSeq++
			curFile = ch
			curChapter = fmt.Sprintf("hd_%d", autoSeq)
			chapterTitle = previewOf(text, 80)
			seq = 0
		} else if ch != curFile {
			flush()
			curFile = ch
			curChapter = ch
			chapterTitle = b.ChapterTitle
			seq = 0
		}

		if utf8.RuneCountInString(text) > target {
			// Oversized block: sentence-split into pieces, one batch per piece.
			flush()
			pieceIdx := 0
			for _, piece := range p.splitText(text) {
				pt := strings.TrimSpace(piece)
				if pt == "" {
					continue
				}
				nb := &batch{
					ID:      fmt.Sprintf("%s@%d", curChapter, seq),
					Chapter: curChapter,
					Title:   chapterTitle,
					Entries: []*batchEntry{{BlockID: b.ID, PieceIndex: pieceIdx, Text: pt}},
				}
				pieceIdx++
				seq++
				nb.Text = pt
				batches = append(batches, nb)
			}
			continue
		}

		if cur == nil {
			cur = &batch{ID: fmt.Sprintf("%s@%d", curChapter, seq), Chapter: curChapter, Title: chapterTitle}
			seq++
		} else if utf8.RuneCountInString(cur.Text)+2+utf8.RuneCountInString(text) > target {
			flush()
			cur = &batch{ID: fmt.Sprintf("%s@%d", curChapter, seq), Chapter: curChapter, Title: chapterTitle}
			seq++
		}
		cur.Entries = append(cur.Entries, &batchEntry{BlockID: b.ID, Text: text})
		cur.Text = joinEntryTexts(cur.Entries)
	}
	flush()
	return batches
}

// buildClassicBatches is the pre-chapter translation plan: every block is
// its own request (oversized blocks are still sentence-split so a single
// huge paragraph cannot overflow the model). Batch IDs intentionally use
// the legacy "{blockID}-{piece}" key format, making checkpoints from the
// per-block era resume without re-translating.
func (p *Processor) buildClassicBatches(blocks []parser.TextBlock) []*batch {
	target := p.cfg.MaxChunkSize
	if target <= 0 {
		target = 2400
	}
	var batches []*batch
	for _, b := range blocks {
		text := strings.TrimSpace(b.OriginalText)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > target {
			pieceIdx := 0
			for _, piece := range p.splitText(text) {
				pt := strings.TrimSpace(piece)
				if pt == "" {
					continue
				}
				batches = append(batches, &batch{
					ID:      fmt.Sprintf("%s-%d", b.ID, pieceIdx),
					Entries: []*batchEntry{{BlockID: b.ID, PieceIndex: pieceIdx, Text: pt}},
					Text:    pt,
				})
				pieceIdx++
			}
			continue
		}
		batches = append(batches, &batch{
			ID:      fmt.Sprintf("%s-0", b.ID),
			Entries: []*batchEntry{{BlockID: b.ID, PieceIndex: 0, Text: text}},
			Text:    text,
		})
	}
	return batches
}

// chainLenCap picks how many batches may share one sequential context chain.
// Chains stay within a chapter; long chapters are cut so that at least ~2
// chains per worker exist, keeping the pool busy while preserving context.
func chainLenCap(totalBatches, workers int) int {
	if totalBatches <= 0 {
		return 1
	}
	if workers < 1 {
		workers = 1
	}
	cap := (totalBatches + workers*2 - 1) / (workers * 2)
	if cap < 2 {
		cap = 2
	}
	if cap > 40 {
		cap = 40
	}
	return cap
}

func buildChains(batches []*batch, maxLen int) [][]*batch {
	var chains [][]*batch
	var cur []*batch
	curChapter := "\x00none"
	for _, b := range batches {
		if b.Chapter != curChapter || len(cur) >= maxLen {
			if len(cur) > 0 {
				chains = append(chains, cur)
			}
			cur = nil
			curChapter = b.Chapter
		}
		cur = append(cur, b)
	}
	if len(cur) > 0 {
		chains = append(chains, cur)
	}
	return chains
}

func tailOf(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[len(runes)-n:])
}

func previewOf(s string, n int) string {
	preview := strings.ReplaceAll(s, "\n", " ")
	runes := []rune(preview)
	if len(runes) > n {
		return string(runes[:n-1]) + "..."
	}
	return preview
}

// legacyEntriesCovered reports whether every entry of the batch has a legacy
// "{blockID}-{piece}" translation in stateMap.
func legacyEntriesCovered(b *batch, stateMap map[string]string) bool {
	if stateMap == nil {
		return false
	}
	for _, e := range b.Entries {
		if strings.TrimSpace(stateMap[e.legacyKey()]) == "" {
			return false
		}
	}
	return true
}

// mapTranslationToEntries splits a batch translation back onto its entries,
// preferring the model's paragraph breaks and falling back to a proportional
// split when the paragraph count does not match.
func mapTranslationToEntries(translated string, entries []*batchEntry) {
	n := len(entries)
	if n == 0 {
		return
	}
	if n == 1 {
		t := strings.TrimSpace(translated)
		entries[0].Translated = t
		entries[0].Covered = t != ""
		return
	}

	if paras := splitParagraphs(translated, "\n\n"); len(paras) == n {
		applyEntryTexts(entries, paras)
		return
	}
	if paras := splitParagraphs(translated, "\n"); len(paras) == n {
		applyEntryTexts(entries, paras)
		return
	}
	applyEntryTexts(entries, proportionalSplit(translated, entries))
}

func applyEntryTexts(entries []*batchEntry, texts []string) {
	for i, e := range entries {
		t := ""
		if i < len(texts) {
			t = strings.TrimSpace(texts[i])
		}
		e.Translated = t
		e.Covered = t != ""
	}
}

func splitParagraphs(s string, sep string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// proportionalSplit divides translated among entries proportionally to the
// original text lengths, breaking at punctuation or whitespace.
func proportionalSplit(translated string, entries []*batchEntry) []string {
	runes := []rune(strings.TrimSpace(translated))
	results := make([]string, len(entries))
	if len(runes) == 0 {
		return results
	}

	totalWeight := 0
	for _, e := range entries {
		w := utf8.RuneCountInString(e.Text)
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	acc := 0
	used := 0
	for i := 0; i < len(entries); i++ {
		if i == len(entries)-1 {
			results[i] = strings.TrimSpace(string(runes[used:]))
			break
		}
		acc += entryWeight(entries[i])
		target := len(runes) * acc / totalWeight
		if target < used {
			target = used
		}
		splitAt := findPunctSplit(runes, target, used)
		remainingEntries := len(entries) - i - 1
		maxSplit := len(runes) - remainingEntries
		if splitAt > maxSplit {
			splitAt = maxSplit
		}
		if splitAt < used {
			splitAt = used
		}
		results[i] = strings.TrimSpace(string(runes[used:splitAt]))
		used = splitAt
	}
	return results
}

func entryWeight(e *batchEntry) int {
	w := utf8.RuneCountInString(e.Text)
	if w <= 0 {
		return 1
	}
	return w
}

func isSplitPunct(r rune) bool {
	switch r {
	case ' ', '\t', '。', '！', '？', '；', '，', '、', '.', '!', '?', ';', ',', ':', '：':
		return true
	}
	return unicode.IsSpace(r)
}

// findPunctSplit finds the closest punctuation/space boundary to target,
// searching backwards then forwards, never going below min.
func findPunctSplit(runes []rune, target, min int) int {
	if target >= len(runes) {
		return len(runes)
	}
	window := 48
	best := target
	bestDist := len(runes) + 1

	for i := target; i >= min && i > target-window; i-- {
		if isSplitPunct(runes[i]) {
			d := target - i
			if d < bestDist {
				bestDist = d
				best = i + 1
			}
			break
		}
	}
	for i := target; i < len(runes) && i < target+window; i++ {
		if isSplitPunct(runes[i]) {
			d := i - target
			if d < bestDist {
				bestDist = d
				best = i + 1
			}
			break
		}
	}
	if best < min {
		return min
	}
	return best
}

// applyFinalEntryTexts resolves each entry's final text: fresh translation,
// then state cache (new batch key or legacy per-piece key), then original.
// wholeLegacy carries whole-block translations recovered from legacy state
// (see normalizeLegacyState): the first piece takes the whole text and the
// remaining pieces are suppressed so the block is not duplicated.
func applyFinalEntryTexts(batches []*batch, stateMap map[string]string, wholeLegacy map[string]string) {
	for _, b := range batches {
		cachedNew := ""
		if stateMap != nil {
			cachedNew = strings.TrimSpace(stateMap[b.ID])
		}
		// Offline reassembly path (no runtime status): map the cached batch
		// text onto entries once. Runtime batches already carry their mapping.
		if cachedNew != "" && b.Status == "" {
			mapTranslationToEntries(cachedNew, b.Entries)
		}
	}

	if len(wholeLegacy) > 0 {
		byBlock := entriesByBlock(batches)
		for blockID, whole := range wholeLegacy {
			entries, ok := byBlock[blockID]
			if !ok || strings.TrimSpace(whole) == "" {
				continue
			}
			// The whole-block legacy translation is authoritative for this
			// block: piece 0 carries it, the rest stay empty.
			for _, e := range entries {
				if e.PieceIndex == 0 {
					e.Translated = strings.TrimSpace(whole)
					e.Covered = true
				} else {
					e.Translated = ""
					e.SuppressOriginal = true
				}
			}
		}
	}

	for _, b := range batches {
		for _, e := range b.Entries {
			if e.Translated == "" && stateMap != nil {
				e.Translated = strings.TrimSpace(stateMap[e.legacyKey()])
			}
			if e.Translated == "" && !e.SuppressOriginal {
				e.Translated = e.Text
			}
		}
	}
}

func entriesByBlock(batches []*batch) map[string][]*batchEntry {
	m := make(map[string][]*batchEntry)
	for _, b := range batches {
		for _, e := range b.Entries {
			m[e.BlockID] = append(m[e.BlockID], e)
		}
	}
	return m
}

// normalizeLegacyState protects sentence-split blocks from stale legacy
// checkpoints. Legacy state keys are "{blockID}-{piece}"; an older run that
// translated an oversized block as ONE unit stored the whole translation
// under "{blockID}-0". Feeding that into the first piece of a fresh split
// would duplicate the block (whole text + the remaining pieces). Per split
// block:
//   - every new piece has a legacy key → aligned resume, keep the keys
//   - only "{blockID}-0" exists          → whole-block legacy; moved into
//     wholeLegacy and removed from the map
//   - anything else (partial mismatch)   → stale; keys dropped so the
//     pieces simply re-translate
//
// The input map is never mutated; a filtered copy is returned.
func normalizeLegacyState(batches []*batch, stateMap map[string]string) (map[string]string, map[string]string) {
	if stateMap == nil || len(stateMap) == 0 {
		return stateMap, nil
	}
	byBlock := entriesByBlock(batches)
	out := make(map[string]string, len(stateMap))
	for k, v := range stateMap {
		out[k] = v
	}
	wholeLegacy := make(map[string]string)
	for blockID, entries := range byBlock {
		split := false
		for _, e := range entries {
			if e.PieceIndex > 0 {
				split = true
				break
			}
		}
		if !split {
			continue
		}
		all := true
		for _, e := range entries {
			if strings.TrimSpace(out[e.legacyKey()]) == "" {
				all = false
				break
			}
		}
		if all {
			continue // aligned resume across pieces
		}
		whole := strings.TrimSpace(out[blockID+"-0"])
		if whole != "" && strings.TrimSpace(out[blockID+"-1"]) == "" {
			// Old run translated the block as one unit before it was split:
			// reuse the whole translation instead of re-translating.
			wholeLegacy[blockID] = whole
		}
		for _, e := range entries {
			delete(out, e.legacyKey())
		}
	}
	if len(wholeLegacy) == 0 {
		wholeLegacy = nil
	}
	return out, wholeLegacy
}

// translatedPiece is one piece of a (possibly sentence-split) block ready
// for final assembly, carrying whether it holds a real translation or just
// an echo of the source.
type translatedPiece struct {
	text       string
	translated bool
}

// assembleBlocks rebuilds translated blocks (in original order) from batches.
// Sentence-split blocks are re-joined with script-aware separators, and a
// block whose every piece ended up untranslated is emitted verbatim as the
// original text so the document parsers' echo guards can recognize it and
// keep it exactly once in bilingual output.
func assembleBlocks(blocks []parser.TextBlock, batches []*batch) []parser.TranslatedBlock {
	parts := make(map[string][]translatedPiece)
	for _, b := range batches {
		for _, e := range b.Entries {
			t := strings.TrimSpace(e.Translated)
			parts[e.BlockID] = append(parts[e.BlockID], translatedPiece{
				text:       e.Translated,
				translated: t != "" && t != strings.TrimSpace(e.Text),
			})
		}
	}

	translatedBlocks := make([]parser.TranslatedBlock, 0, len(blocks))
	for _, b := range blocks {
		ps, ok := parts[b.ID]
		if ok && len(ps) > 0 {
			allEcho := true
			for _, pc := range ps {
				if pc.translated {
					allEcho = false
					break
				}
			}
			if allEcho {
				// Nothing was actually translated: emit the original verbatim
				// (never a lossy re-join of echoed pieces) so bilingual assembly
				// recognizes it as untranslated and skips the injection.
				translatedBlocks = append(translatedBlocks, parser.TranslatedBlock{
					ID:             b.ID,
					TranslatedText: b.OriginalText,
				})
				continue
			}
			translatedBlocks = append(translatedBlocks, parser.TranslatedBlock{
				ID:             b.ID,
				TranslatedText: joinPieces(ps),
			})
			continue
		}
		translatedBlocks = append(translatedBlocks, parser.TranslatedBlock{
			ID:             b.ID,
			TranslatedText: b.OriginalText,
		})
	}
	return translatedBlocks
}

// joinPieces re-assembles the pieces of a sentence-split block. Empty pieces
// (suppressed by whole-block legacy recovery) are skipped; a space is only
// inserted between two Latin-script runs — CJK text needs no separator and
// sentence punctuation already provides the boundary.
func joinPieces(ps []translatedPiece) string {
	var sb strings.Builder
	var last rune
	written := false
	for _, pc := range ps {
		if strings.TrimSpace(pc.text) == "" {
			continue
		}
		first := firstRuneOf(pc.text)
		if written && needsJoinSpace(last, first) {
			sb.WriteString(" ")
		}
		sb.WriteString(pc.text)
		last = lastRuneOf(pc.text)
		written = true
	}
	return sb.String()
}

func needsJoinSpace(a, b rune) bool {
	if a == 0 || b == 0 {
		return false
	}
	return !isCJK(a) && !isCJK(b)
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		(r >= 0x3000 && r <= 0x303F) || // CJK punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // fullwidth forms
}

func lastRuneOf(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}

func firstRuneOf(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// Reassemble rebuilds translated blocks purely from a completed-batches map
// (no model calls). Missing batches fall back to legacy per-block keys, then
// to the original text.
func (p *Processor) Reassemble(blocks []parser.TextBlock, stateMap map[string]string) []parser.TranslatedBlock {
	batches := p.buildBatches(blocks)
	stateMap, wholeLegacy := normalizeLegacyState(batches, stateMap)
	applyFinalEntryTexts(batches, stateMap, wholeLegacy)
	return assembleBlocks(blocks, batches)
}

// Process handles chapter batching, concurrent translation and reassembly.
// Batches of one chapter are processed sequentially in a chain (sharing
// rolling context); chains run in parallel on the worker pool.
func (p *Processor) Process(ctx context.Context, blocks []parser.TextBlock, stateMap map[string]string, onProgress func(int, int, string), onChunkCompleted func(string, string)) ([]parser.TranslatedBlock, TranslationStats, error) {
	var stats TranslationStats
	if ctx == nil {
		ctx = context.Background()
	}

	batches := p.buildBatches(blocks)
	// Neutralize stale legacy checkpoints that would corrupt split blocks
	// (whole-block translations landing on the first piece) before any
	// cache lookups run.
	stateMap, wholeLegacy := normalizeLegacyState(batches, stateMap)
	total := len(batches)
	if onProgress != nil {
		onProgress(0, total, "")
	}

	workers := p.cfg.Concurrency
	if workers < 1 {
		workers = 1
	}
	chains := buildChains(batches, chainLenCap(total, workers))
	if !p.cfg.ChapterBatching {
		// Classic mode: no rolling context — every request stands alone, so
		// chains of length 1 give full parallelism.
		chains = buildChains(batches, 1)
	}
	chainCh := make(chan []*batch, len(chains))
	for _, c := range chains {
		chainCh <- c
	}
	close(chainCh)

	var completed int64
	// mu serializes stats updates and onProgress callbacks so callers can use
	// non-thread-safe state inside the callback.
	var mu sync.Mutex

	emitProgress := func(current int, msg string) {
		if onProgress != nil {
			onProgress(current, total, msg)
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chain := range chainCh {
				prevTail := ""
				for _, b := range chain {
					if ctx.Err() != nil {
						b.Status = translator.StatusFailed
						b.Err = ctx.Err()
						mu.Lock()
						n := atomic.AddInt64(&completed, 1)
						emitProgress(int(n), fmt.Sprintf("⏸️ [批 %s] 任务已暂停，恢复后将从本批继续", b.ID))
						mu.Unlock()
						continue
					}

					// Whole-block legacy checkpoint: an older run translated this
					// block as one unit. Piece 0 carries the whole text and every
					// other piece of the block is suppressed so the block is
					// emitted exactly once.
					if len(b.Entries) > 0 {
						if whole, ok := wholeLegacy[b.Entries[0].BlockID]; ok {
							for _, e := range b.Entries {
								if e.PieceIndex == 0 {
									e.Translated = whole
									e.Covered = true
									b.Translated = whole
								} else {
									e.Translated = ""
									e.SuppressOriginal = true
								}
							}
							b.Status = translator.StatusSuccess
							b.FromCache = true
							prevTail = tailOf(whole, 600)
							mu.Lock()
							n := atomic.AddInt64(&completed, 1)
							stats.SuccessCount++
							emitProgress(int(n), fmt.Sprintf("⏭️ [批 %s] 命中旧版整段缓存，跳过翻译", b.ID))
							mu.Unlock()
							continue
						}
					}

					if stateMap != nil && strings.TrimSpace(stateMap[b.ID]) != "" {
						b.Translated = strings.TrimSpace(stateMap[b.ID])
						b.Status = translator.StatusSuccess
						b.FromCache = true
						mapTranslationToEntries(b.Translated, b.Entries)
						prevTail = tailOf(b.Translated, 600)
						mu.Lock()
						n := atomic.AddInt64(&completed, 1)
						stats.SuccessCount++
						emitProgress(int(n), fmt.Sprintf("⏭️ [批 %s] 断点续传命中缓存，跳过翻译", b.ID))
						mu.Unlock()
						continue
					}

					if legacyEntriesCovered(b, stateMap) {
						var sb strings.Builder
						for i, e := range b.Entries {
							if i > 0 {
								sb.WriteString("\n\n")
							}
							t := strings.TrimSpace(stateMap[e.legacyKey()])
							e.Translated = t
							e.Covered = true
							sb.WriteString(t)
						}
						b.Translated = sb.String()
						b.Status = translator.StatusSuccess
						b.FromCache = true
						prevTail = tailOf(b.Translated, 600)
						mu.Lock()
						n := atomic.AddInt64(&completed, 1)
						stats.SuccessCount++
						emitProgress(int(n), fmt.Sprintf("⏭️ [批 %s] 命中旧版断点缓存，跳过翻译", b.ID))
						mu.Unlock()
						continue
					}

					translated, status, err := p.translator.TranslateBatch(ctx, translator.TranslateRequest{
						Text:           b.Text,
						ParagraphCount: len(b.Entries),
						ChapterTitle:   b.Title,
						PrevTail:       prevTail,
					}, func(msg string) {
						mu.Lock()
						emitProgress(-1, fmt.Sprintf("[批 %s] %s", b.ID, msg))
						mu.Unlock()
					})
					b.Translated = translated
					b.Status = status
					b.Err = err
					mapTranslationToEntries(b.Translated, b.Entries)

					mu.Lock()
					n := atomic.AddInt64(&completed, 1)
					switch {
					case err == nil && status == translator.StatusSuccess:
						prevTail = tailOf(translated, 600)
						emitProgress(int(n), fmt.Sprintf("✅ [批 %s] 完成: %s", b.ID, previewOf(translated, 60)))
						stats.SuccessCount++
					case err == nil && status == translator.StatusFallback:
						// Bypassed / model echoed the source text: keep the
						// original, count as degradation, not as a failure.
						emitProgress(int(n), fmt.Sprintf("⚠️ [批 %s] 模型未翻译，已保留原文（计入降级统计）", b.ID))
						stats.FallbackCount++
					case ctx.Err() != nil || errors.Is(err, context.Canceled):
						// Paused/cancelled mid-flight: not a translation
						// failure — the batch simply resumes later.
						b.Status = translator.StatusFailed
						emitProgress(int(n), fmt.Sprintf("⏸️ [批 %s] 任务已暂停，恢复后将从本批继续", b.ID))
					default:
						emitProgress(int(n), fmt.Sprintf("❌ [批 %s] 翻译完全失败，准备降级容错。", b.ID))
						if status == translator.StatusRefused {
							stats.RefusedCount++
						} else {
							stats.FailureCount++
						}
						stats.FailedBlocks = append(stats.FailedBlocks, FailedBlockInfo{
							ID: b.ID, OriginalText: b.Text, Error: errText(err),
						})
					}
					mu.Unlock()

					if err == nil && status == translator.StatusSuccess && onChunkCompleted != nil {
						onChunkCompleted(b.ID, b.Translated)
					}
				}
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, stats, ctx.Err()
	}

	// Second-chance pass: paragraphs the model left untranslated (echo,
	// refusal, failure or mapping gaps) are retried as single-paragraph
	// requests before the original text is allowed to stay in the output.
	p.retryUntranslated(ctx, batches, &stats, &mu, emitProgress, onChunkCompleted)
	if ctx.Err() != nil {
		return nil, stats, ctx.Err()
	}

	failedCount := 0
	for _, b := range batches {
		if b.Err != nil {
			failedCount++
		}
	}
	if failedCount > 0 && onProgress != nil {
		onProgress(-1, total, fmt.Sprintf("⚠️ 警告: %d 个文本块翻译失败，已降级为原文保留", failedCount))
	}

	applyFinalEntryTexts(batches, stateMap, wholeLegacy)
	translatedBlocks := assembleBlocks(blocks, batches)
	return translatedBlocks, stats, nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// retryRounds caps how many times the second-chance pass re-sends a still
// untranslated paragraph.
const retryRounds = 2

// entryReallyTranslated reports whether the entry carries an actual
// translation: non-empty, different from its source, and — for
// Chinese-output roles translating non-Han text — actually containing Han
// runes instead of a same-language rewrite. Bypassed texts (URLs,
// digits-only, ...) need no translation and count as covered.
func (p *Processor) entryReallyTranslated(e *batchEntry) bool {
	if translator.BypassesTranslation(e.Text) {
		return true
	}
	t := strings.TrimSpace(e.Translated)
	if t == "" || t == strings.TrimSpace(e.Text) {
		return false
	}
	if translator.TargetsChinese(p.cfg.Prompt) && !translator.ContainsHan(e.Text) &&
		utf8.RuneCountInString(e.Text) >= 10 && !translator.ContainsHan(t) {
		return false
	}
	return true
}

func (p *Processor) batchReallyTranslated(b *batch) bool {
	for _, e := range b.Entries {
		if !p.entryReallyTranslated(e) {
			return false
		}
	}
	return true
}

// retryUntranslated gives batches that did not yield a real translation a
// second chance: every still-untranslated entry is re-sent as a
// single-paragraph request, which small local models handle far more
// reliably than multi-paragraph batches. Fully recovered batches are
// persisted like normal successes; the rest keep their degraded result.
func (p *Processor) retryUntranslated(ctx context.Context, batches []*batch, stats *TranslationStats, mu *sync.Mutex, emit func(int, string), onChunkCompleted func(string, string)) {
	type retryJob struct {
		b *batch
		e *batchEntry
	}

	pendingJobs := func() []retryJob {
		var jobs []retryJob
		for _, b := range batches {
			if b.FromCache {
				continue
			}
			for _, e := range b.Entries {
				if !p.entryReallyTranslated(e) {
					jobs = append(jobs, retryJob{b: b, e: e})
				}
			}
		}
		return jobs
	}

	runRound := func(jobs []retryJob, workers int) {
		jobCh := make(chan *retryJob, len(jobs))
		for i := range jobs {
			jobCh <- &jobs[i]
		}
		close(jobCh)
		if workers > len(jobs) {
			workers = len(jobs)
		}
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobCh {
					if ctx.Err() != nil {
						return
					}
					translated, status, err := p.translator.TranslateBatch(ctx, translator.TranslateRequest{
						Text:           j.e.Text,
						ParagraphCount: 1,
						// Retry requests deliberately carry no chapter context:
						// small models comply far better with a minimal prompt,
						// and they tend to echo context markers into the
						// translation otherwise.
					}, func(msg string) {
						mu.Lock()
						emit(-1, fmt.Sprintf("[批 %s·重试] %s", j.b.ID, msg))
						mu.Unlock()
					})
					if err != nil {
						continue
					}
					t := strings.TrimSpace(translated)
					if status == translator.StatusSuccess && t != "" && t != strings.TrimSpace(j.e.Text) {
						j.e.Translated = t
						j.e.Covered = true
					}
				}
			}()
		}
		wg.Wait()
	}

	initial := pendingJobs()
	if len(initial) == 0 {
		return
	}
	workers := p.cfg.Concurrency
	if workers < 1 {
		workers = 1
	}
	mu.Lock()
	emit(-1, fmt.Sprintf("🔁 检测到 %d 个未翻译段落，拆分为单段二次重试...", len(initial)))
	mu.Unlock()

	remaining := initial
	for round := 1; round <= retryRounds && len(remaining) > 0; round++ {
		runRound(remaining, workers)
		if ctx.Err() != nil {
			return
		}
		remaining = pendingJobs()
	}

	// Finalize: flip fully recovered batches back to success, persist them,
	// and report the ones that are still untranslated.
	for _, b := range batches {
		if b.FromCache {
			continue
		}
		hadPending := false
		for _, j := range initial {
			if j.b == b {
				hadPending = true
				break
			}
		}
		if !hadPending {
			continue
		}
		recovered := p.batchReallyTranslated(b)
		mu.Lock()
		if recovered {
			if b.Status != translator.StatusSuccess {
				switch b.Status {
				case translator.StatusRefused:
					stats.RefusedCount--
				case translator.StatusFailed:
					stats.FailureCount--
				case translator.StatusFallback:
					stats.FallbackCount--
				}
				stats.SuccessCount++
				b.Status = translator.StatusSuccess
				b.Err = nil
				filtered := stats.FailedBlocks[:0]
				for _, fb := range stats.FailedBlocks {
					if fb.ID != b.ID {
						filtered = append(filtered, fb)
					}
				}
				stats.FailedBlocks = filtered
			}
			b.Translated = joinEntryTexts(b.Entries)
			emit(-1, fmt.Sprintf("✅ [批 %s] 二次重试成功，全部段落已翻译", b.ID))
		} else {
			emit(-1, fmt.Sprintf("⚠️ [批 %s] 二次重试后仍有未翻译段落，保留原文", b.ID))
		}
		mu.Unlock()
		if recovered && onChunkCompleted != nil {
			onChunkCompleted(b.ID, b.Translated)
		}
	}
}

func (p *Processor) splitText(text string) []string {
	maxLen := p.cfg.MaxChunkSize
	if maxLen <= 0 {
		maxLen = 2400
	}
	if utf8.RuneCountInString(text) <= maxLen {
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
		if utf8.RuneCountInString(part) > maxLen {
			flushChunk()
			subParts := splitByWeakSeparators(part, maxLen)
			result = append(result, subParts...)
			continue
		}
		if utf8.RuneCountInString(currentChunk.String())+utf8.RuneCountInString(part) > maxLen {
			flushChunk()
		}
		if currentChunk.Len() > 0 {
			if needsJoinSpace(lastRuneOf(currentChunk.String()), firstRuneOf(part)) {
				currentChunk.WriteString(" ")
			}
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
				if isAbbreviationDot(runes, i) {
					continue
				}
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

// abbreviationWords are tokens after which a dot is part of the token, not
// a sentence end ("Mr. Smith", "etc. items", "approx. value").
var abbreviationWords = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
	"sr": true, "jr": true, "st": true, "mt": true, "rev": true,
	"gen": true, "col": true, "capt": true, "lt": true, "sgt": true,
	"vs": true, "etc": true, "eg": true, "ie": true, "al": true,
	"fig": true, "figs": true, "vol": true, "ch": true, "sec": true,
	"ed": true, "eds": true, "inc": true, "ltd": true, "co": true,
	"corp": true, "dept": true, "univ": true, "approx": true,
	"ibid": true, "viz": true, "cf": true,
}

// isAbbreviationDot reports whether the '.' at runes[dotIdx] belongs to an
// abbreviation rather than ending a sentence. Two shapes are recognized:
// a known abbreviation word ("Mr.", "etc."), and initial-letter sequences
// ("U.S.", "J. K.") where a single letter is followed by the dot. Decimal
// points ("3.14") have no letter token and fall through untouched.
func isAbbreviationDot(runes []rune, dotIdx int) bool {
	j := dotIdx - 1
	var tok []rune
	for j >= 0 && unicode.IsLetter(runes[j]) && len(tok) < 8 {
		tok = append([]rune{runes[j]}, tok...)
		j--
	}
	if len(tok) == 0 {
		return false // decimal point, ellipsis, etc.
	}
	if len(tok) == 1 {
		return true // "U.S." / "J. K." initial style
	}
	return abbreviationWords[strings.ToLower(string(tok))]
}

// splitByWeakSeparators breaks an oversized sentence at clause punctuation
// (commas, semicolons, colons) and, failing that, at a whitespace word
// boundary — never through the middle of a word. A run with no whitespace
// at all (e.g. a very long URL) is the only case cut at exactly maxLen,
// because there is no safer boundary to find.
func splitByWeakSeparators(text string, maxLen int) []string {
	runes := []rune(text)
	var result []string
	start := 0
	for start < len(runes) {
		if len(runes)-start <= maxLen {
			part := strings.TrimSpace(string(runes[start:]))
			if part != "" {
				result = append(result, part)
			}
			break
		}
		cut := -1
		minCut := start + maxLen/4 // avoid degenerate slivers
		// Prefer the last clause punctuation inside the window.
		for i := start + maxLen - 1; i > minCut; i-- {
			if isWeakSeparator(runes[i]) {
				cut = i + 1
				break
			}
		}
		// Fall back to the last whitespace inside the window (word boundary).
		if cut < 0 {
			for i := start + maxLen - 1; i > minCut; i-- {
				if unicode.IsSpace(runes[i]) {
					cut = i + 1
					break
				}
			}
		}
		// Look a little further ahead for the next word boundary so the
		// overflow stays bounded instead of cutting a word in half.
		if cut < 0 {
			for i := start + maxLen; i < len(runes) && i <= start+maxLen+maxLen/4; i++ {
				if unicode.IsSpace(runes[i]) {
					cut = i + 1
					break
				}
			}
		}
		if cut < 0 {
			cut = start + maxLen // whitespace-free run: hard cut
		}
		part := strings.TrimSpace(string(runes[start:cut]))
		if part != "" {
			result = append(result, part)
		}
		start = cut
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
