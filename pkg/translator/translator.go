package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"auto_translate/pkg/config"
)

type TranslationStatus string

const (
	StatusSuccess  TranslationStatus = "success"
	StatusFallback TranslationStatus = "fallback"
	StatusRefused  TranslationStatus = "refused"
	StatusFailed   TranslationStatus = "failed"
	StatusSkip     TranslationStatus = "skip"
)

// Engine identifiers re-exported for callers of this package.
const (
	EngineOllama = config.EngineOllama
	EngineMLX    = config.EngineMLX
	EngineOmlx   = config.EngineOmlx
)

// TranslateRequest carries one translation batch plus chapter context.
type TranslateRequest struct {
	// Text is the raw content to translate. When ParagraphCount > 1 the
	// paragraphs are joined with blank lines and the model is instructed to
	// keep the same paragraph structure.
	Text           string
	ParagraphCount int
	// ChapterTitle is the current chapter heading, used as context only.
	ChapterTitle string
	// PrevTail is the tail of the previous batch's translation in the same
	// chapter, used as rolling context only.
	PrevTail string
}

// Translator handles HTTP requests to the LLM backend and glossary enforcement.
type Translator struct {
	cfg    *config.Config
	client *http.Client
}

// kwargsSupport caches, per chat-completions URL, whether the server
// accepts the (non-standard) chat_template_kwargs field. Sending it to a
// strictly-validating server turns every request into a 400 and kills the
// whole run, so the field is only sent while the server is known (or
// presumed) to accept it; a 4xx rejection disables it for the process.
var (
	kwargsMu      sync.Mutex
	kwargsSupport = make(map[string]bool)
)

func kwargsAllowed(url string) bool {
	kwargsMu.Lock()
	defer kwargsMu.Unlock()
	supported, known := kwargsSupport[url]
	return !known || supported
}

func markKwargsRejected(url string) {
	kwargsMu.Lock()
	defer kwargsMu.Unlock()
	kwargsSupport[url] = false
}

// payloadHasKwargs reports whether the marshaled request body carries the
// chat_template_kwargs field.
func payloadHasKwargs(data []byte) bool {
	return bytes.Contains(data, []byte("chat_template_kwargs"))
}

var latinDoubleDashPattern = regexp.MustCompile(`([A-Za-z])[—–-]{2,}([A-Za-z])`)
var rePrefixBeforeHanPattern = regexp.MustCompile(`(?i)\bre\s*[—–-]?\s*([\p{Han}])`)

// reThinkingBlock matches a complete thinking block (Qwen <think>,
// DeepSeek <thinking>, R1-style <reasoning>). Go's RE2 has no
// backreferences, so the three tag names are enumerated.
// DeepSeek <thinking>, R1-style <reasoning>).
var reThinkingBlock = regexp.MustCompile(`(?is)<\s*(?:think|thinking|reasoning)\s*>.*?<\s*/\s*(?:think|thinking|reasoning)\s*>`)

// reUnterminatedThink matches an opening tag with no closing partner:
// the model ran out of tokens inside its thinking, so everything after it
// is thinking residue rather than a translation.
var reUnterminatedThink = regexp.MustCompile(`(?s)^\s*<\s*(?:think|thinking|reasoning)\s*>.*`)

// Prompt-block leak markers: a confused small model sometimes answers with
// a fragment of the system prompt instead of a translation. The injected
// control blocks are recognizable by their bracketed markers; the glossary
// and format blocks always end with a full stop.
var (
	reGlossaryBlockLeak = regexp.MustCompile(`(?m)\[术语表[^\n]*?(。|$)`)
	reFormatBlockLeak  = regexp.MustCompile(`(?m)\[格式要求\][^\n]*`)
)

// stripPromptBlockLeaks removes echoed glossary / format-requirement
// blocks from a model answer. It reports whether anything other than those
// prompt fragments remains — an answer consisting solely of them is a
// prompt echo, not a translation.
func stripPromptBlockLeaks(s string) (string, bool) {
	if !strings.Contains(s, "[术语表") && !strings.Contains(s, "[格式要求]") {
		return s, true
	}
	cleaned := reGlossaryBlockLeak.ReplaceAllString(s, "")
	cleaned = reFormatBlockLeak.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, cleaned != ""
}

// StripThinkingBlocks removes model thinking blocks from a response. A
// leading unterminated block swallows the whole string (the model never got
// to the translation); complete blocks are removed wherever they appear.
func StripThinkingBlocks(s string) string {
	s = reThinkingBlock.ReplaceAllString(s, "")
	s = reUnterminatedThink.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

var hanReHanPattern = regexp.MustCompile(`([\p{Han}])\s*(?i:re)\s*[—–-]?\s*([\p{Han}])`)

// New creates a new Translator instance.
func New(cfg *config.Config) *Translator {
	return &Translator{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.RequestTimeoutSec) * time.Second,
		},
	}
}

func (t *Translator) engine() string {
	return config.ResolveEngine(t.cfg.APIURL, t.cfg.Model, t.cfg.Engine)
}

// apiKey returns the bearer token for engines that require one. For oMLX the
// key is read from ~/.omlx/settings.json when not configured explicitly, but
// the auto-read key is only attached to local addresses so it never leaks to
// a custom remote endpoint.
func (t *Translator) apiKey() string {
	if key := strings.TrimSpace(t.cfg.APIKey); key != "" {
		return key
	}
	if t.engine() == config.EngineOmlx && isLocalHostURL(t.cfg.APIURL) {
		return config.ReadOMLXAPIKey()
	}
	return ""
}

func isLocalHostURL(u string) bool {
	l := strings.ToLower(u)
	return l == "" ||
		strings.Contains(l, "127.0.0.1") ||
		strings.Contains(l, "localhost") ||
		strings.Contains(l, "[::1]")
}

// Translate translates a single piece of text without chapter context.
// Retained as a thin wrapper for compatibility.
func (t *Translator) Translate(ctx context.Context, text string, onEvent ...func(string)) (string, TranslationStatus, error) {
	return t.TranslateBatch(ctx, TranslateRequest{Text: text, ParagraphCount: 1}, onEvent...)
}

// TranslateBatch translates a batch (one or more paragraphs) with optional
// chapter context. Implements retries and glossary enforcement via prompt.
func (t *Translator) TranslateBatch(ctx context.Context, req TranslateRequest, onEvent ...func(string)) (string, TranslationStatus, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return "", StatusSkip, nil // skip empty chunks
	}
	if shouldBypassTranslation(text) {
		return text, StatusFallback, nil
	}

	var ev func(string)
	if len(onEvent) > 0 {
		ev = onEvent[0]
	}

	// 0. Short Text / Glossary Fallback Strategy (single paragraph only)
	if req.ParagraphCount <= 1 {
		runes := []rune(text)
		if len(runes) < 20 {
			// Priority 1: Check Glossary for exact match
			for en, cn := range t.cfg.Glossary {
				if strings.EqualFold(text, strings.TrimSpace(en)) {
					return cn, StatusFallback, nil
				}
			}
			// Priority 2: If extremely short and no spaces, return as-is
			if len(runes) < 5 && !strings.Contains(text, " ") {
				if !isASCIILowerWord(text) {
					return text, StatusFallback, nil
				}
			}
		}
	}

	requestURL, payload := t.buildRequest(req)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", StatusFailed, fmt.Errorf("failed to marshal payload: %w", err)
	}

	var translated string
	maxRetries := t.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx == nil {
			ctx = context.Background()
		}
		reqHTTP, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return "", StatusFailed, fmt.Errorf("failed to create request: %w", err)
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		if key := t.apiKey(); key != "" {
			reqHTTP.Header.Set("Authorization", "Bearer "+key)
		}

		// Heartbeat to prevent silent hanging feeling
		doneCh := make(chan struct{})
		go func(att int) {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			elapsed := 0
			for {
				select {
				case <-doneCh:
					return
				case <-ticker.C:
					elapsed += 15
					if ev != nil {
						ev(fmt.Sprintf("⏳ 仍在生成中... (已耗时 %ds, 尚未超时) [Attempt %d/%d]", elapsed, att, maxRetries))
					}
				}
			}
		}(attempt)

		resp, err := t.client.Do(reqHTTP)
		close(doneCh)

		if err != nil {
			if ctx.Err() != nil {
				// Cancelled/paused: abort instead of burning retry backoff.
				return "", StatusFailed, fmt.Errorf("API request cancelled: %w", ctx.Err())
			}
			if attempt == maxRetries {
				return "", StatusFailed, fmt.Errorf("API request failed after %d attempts: %w", maxRetries, err)
			}
			if ev != nil {
				ev(fmt.Sprintf("API request failed (Attempt %d/%d): %v. Retrying...", attempt, maxRetries, err))
			}
			if !sleepCtx(ctx, attempt) {
				return "", StatusFailed, fmt.Errorf("API request cancelled during retry: %w", ctx.Err())
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if ctx.Err() != nil {
				return "", StatusFailed, fmt.Errorf("API request cancelled: %w", ctx.Err())
			}
			// Capability downgrade: a 4xx on a request carrying
			// chat_template_kwargs usually means the server rejects the
			// unknown field. Drop it process-wide and retry this attempt
			// immediately instead of burning the whole retry budget.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && payloadHasKwargs(jsonData) {
				markKwargsRejected(requestURL)
				if ev != nil {
					ev(fmt.Sprintf("服务器不支持 chat_template_kwargs (HTTP %d)，已自动降级并重试", resp.StatusCode))
				}
				_, payload = t.buildRequest(req)
				jsonData, err = json.Marshal(payload)
				if err != nil {
					return "", StatusFailed, fmt.Errorf("failed to marshal payload: %w", err)
				}
				attempt-- // capability probe does not consume an attempt
				continue
			}
			if attempt == maxRetries {
				return "", StatusFailed, fmt.Errorf("API returned non-200 status %d after %d attempts", resp.StatusCode, maxRetries)
			}
			if ev != nil {
				ev(fmt.Sprintf("API returned status %d (Attempt %d/%d). Retrying...", resp.StatusCode, attempt, maxRetries))
			}
			if !sleepCtx(ctx, attempt) {
				return "", StatusFailed, fmt.Errorf("API request cancelled during retry: %w", ctx.Err())
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", StatusFailed, fmt.Errorf("failed to read API response body: %w", err)
		}

		// Parse response. Both OpenAI-style ("choices") and Ollama-style
		// ("message") payloads are accepted.
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return "", StatusFailed, fmt.Errorf("failed to decode API response JSON: %w (body: %s)", err, string(body))
		}

		if len(result.Choices) > 0 {
			translated = result.Choices[0].Message.Content
		} else if strings.TrimSpace(result.Message.Content) != "" {
			translated = result.Message.Content
		} else {
			return "", StatusFailed, fmt.Errorf("API returned empty choices/message")
		}

		// Defensive: strip thinking blocks some servers prepend to content
		// even when thinking was requested off.
		translated = StripThinkingBlocks(translated)

		if strings.Contains(translated, "请提供需要翻译的文本") ||
			strings.Contains(translated, "无法翻译") ||
			strings.Contains(translated, "未提供上下文") ||
			strings.Contains(translated, "没有任何内容") ||
			strings.Contains(translated, "请提供包含") {
			return text, StatusRefused, fmt.Errorf("model refused to translate (fallback to original): %s", translated)
		}

		// Echo detection: small models sometimes return the source text verbatim
		// instead of translating. Surface it as a fallback so the stats show it.
		if strings.TrimSpace(translated) == strings.TrimSpace(text) && strings.TrimSpace(text) != "" {
			return text, StatusFallback, nil
		}
		break
	}

	// 1. Format cleaning (strip markdown blocks if any sneaked in and simple prefix stripping)
	for i := 0; i < 2; i++ {
		translated = strings.TrimSpace(translated)
		if strings.HasPrefix(strings.ToLower(translated), "here is the translation:") {
			translated = translated[len("here is the translation:"):]
		}
		translated = strings.TrimSpace(translated)
		if strings.HasPrefix(translated, "```markdown") {
			translated = translated[len("```markdown"):]
		} else if strings.HasPrefix(translated, "```") {
			translated = translated[len("```"):]
		}
	}
	translated = strings.TrimSpace(translated)
	translated = strings.TrimSuffix(translated, "```")
	translated = strings.TrimSpace(translated)
	translated = latinDoubleDashPattern.ReplaceAllString(translated, "$1-$2")
	translated = hanReHanPattern.ReplaceAllString(translated, "$1$2")
	translated = rePrefixBeforeHanPattern.ReplaceAllString(translated, "$1")
	translated = stripContextMarkerLeaks(translated, req)

	// Prompt-echo guard: a model that answered with the injected glossary
	// or format block produced no translation at all. Keep the source text
	// (fallback) so the processor's single-paragraph retry pass takes over
	// — and so the garbage is never cached as a success.
	if cleaned, hasTranslation := stripPromptBlockLeaks(translated); !hasTranslation {
		return text, StatusFallback, nil
	} else if cleaned != translated {
		translated = cleaned
	}

	// Same-language paraphrase detection: a Chinese-output role given
	// non-Han source must produce Han text. An English rewrite of English
	// text means the model failed to translate — surface it as a fallback
	// so the processor's retry pass can take over.
	if TargetsChinese(t.cfg.Prompt) && !ContainsHan(text) &&
		utf8.RuneCountInString(text) >= 10 && !ContainsHan(translated) {
		return text, StatusFallback, nil
	}

	return translated, StatusSuccess, nil
}

// buildRequest assembles the request URL and JSON payload for the active engine.
func (t *Translator) buildRequest(req TranslateRequest) (string, map[string]interface{}) {
	messages := []map[string]string{
		{"role": "system", "content": t.buildSystemPrompt(req)},
		{"role": "user", "content": req.Text},
	}
	if t.engine() == EngineOllama {
		return toOllamaChatURL(t.cfg.APIURL), map[string]interface{}{
			"model":    t.cfg.Model,
			"messages": messages,
			"stream":   false,
			"think":    false,
			"options": map[string]interface{}{
				"temperature": t.cfg.Temperature,
				"num_ctx":     8192,
			},
		}
	}
	payload := map[string]interface{}{
		"model":       t.cfg.Model,
		"messages":    messages,
		"temperature": t.cfg.Temperature,
		"stream":      false,
		"max_tokens":  batchMaxTokens(req.Text),
	}
	if t.engine() == config.EngineOmlx {
		// oMLX sampling defaults can go stale server-side (observed: a
		// corrupted min_p=1.05 that silently empties every non-stream
		// response with HTTP 200). Explicitly pinning the sampler works
		// around it and matches the Hy-MT2 recommended settings.
		payload["top_p"] = 0.6
		payload["top_k"] = 20
		payload["min_p"] = 0.0
		payload["repetition_penalty"] = 1.05
	}
	// Qwen3-style hybrid models default to thinking ON in their chat
	// template; for translation that only burns tokens and latency. This is
	// a non-standard OpenAI field: MLX-LM / vLLM accept it, but strict
	// servers reject unknown fields with 4xx — so it is only sent while the
	// server accepts it (see kwargsSupport) and is dropped automatically on
	// the first rejection.
	if kwargsAllowed(t.openAIChatURL()) {
		payload["chat_template_kwargs"] = map[string]interface{}{
			"enable_thinking": false,
		}
	}
	return t.openAIChatURL(), payload
}

// buildSystemPrompt combines the role prompt with glossary, paragraph-format
// and chapter-context instructions.
func (t *Translator) buildSystemPrompt(req TranslateRequest) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(t.cfg.Prompt))

	if len(t.cfg.Glossary) > 0 {
		terms := make([]string, 0, len(t.cfg.Glossary))
		for en := range t.cfg.Glossary {
			terms = append(terms, en)
		}
		sort.Strings(terms)
		if len(terms) > 200 {
			terms = terms[:200]
		}
		sb.WriteString("\n\n[术语表·必须严格遵守] ")
		for i, en := range terms {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(en + "=" + t.cfg.Glossary[en])
		}
		sb.WriteString("。")
	}

	if req.ParagraphCount > 1 {
		sb.WriteString(fmt.Sprintf("\n\n[格式要求] 输入由 %d 个段落组成，段落之间以空行分隔。译文必须一一对应地输出相同数量的段落，段落之间同样用一个空行分隔；禁止合并、拆分、增删段落。", req.ParagraphCount))
	}

	if req.ChapterTitle != "" {
		sb.WriteString("\n\n[当前章节] " + req.ChapterTitle + "（仅供理解上下文，不要翻译或输出此行）")
	}
	if req.PrevTail != "" {
		sb.WriteString("\n\n[前文译文结尾·仅供参考，禁止翻译或输出] " + req.PrevTail)
	}
	return sb.String()
}

// batchMaxTokens gives the model enough room to translate the batch without
// hitting a small server-side default.
func batchMaxTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	limit := runes * 3
	if limit < 1024 {
		limit = 1024
	}
	if limit > 8192 {
		limit = 8192
	}
	return limit
}

// sleepCtx waits for the retry backoff, aborting early when ctx is cancelled.
// Returns false when the context was cancelled.
func sleepCtx(ctx context.Context, attempt int) bool {
	sleepSec := attempt * 3
	if sleepSec > 15 {
		sleepSec = 15
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(sleepSec) * time.Second):
		return true
	}
}

func toOllamaChatURL(apiURL string) string {
	if strings.Contains(apiURL, "/api/chat") {
		return apiURL
	}
	if strings.Contains(apiURL, "/v1/chat/completions") {
		return strings.Replace(apiURL, "/v1/chat/completions", "/api/chat", 1)
	}
	return strings.TrimRight(apiURL, "/") + "/api/chat"
}

// openAIChatURL resolves the endpoint for MLX/oMLX (OpenAI-compatible)
// engines. oMLX URLs are commonly given as the base form
// "http://127.0.0.1:8000/v1" (or a bare host), so the chat completions path
// is appended for the omlx engine; other engines post to api_url as-is.
func (t *Translator) openAIChatURL() string {
	u := strings.TrimSpace(t.cfg.APIURL)
	if u == "" {
		return config.DefaultOMLXChatEndpoint
	}
	if t.engine() != config.EngineOmlx {
		return u
	}
	if strings.HasSuffix(u, "/chat/completions") {
		return u
	}
	if strings.HasSuffix(u, "/v1") {
		return u + "/chat/completions"
	}
	// Bare host without any path (e.g. http://127.0.0.1:8000).
	return strings.TrimRight(u, "/") + "/v1/chat/completions"
}

// BypassesTranslation reports whether the text needs no translation at all
// (empty, URL-like, filename-like, or containing no letters). The processor
// uses it to skip pointless retries for such entries.
func BypassesTranslation(text string) bool {
	return shouldBypassTranslation(text)
}

// ContainsHan reports whether s contains at least one Han rune.
func ContainsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// TargetsChinese reports whether the role prompt asks for Chinese output.
// All bundled expert prompts do; empty or non-Chinese prompts disable the
// same-language paraphrase detection.
func TargetsChinese(prompt string) bool {
	return strings.Contains(prompt, "中文") || strings.Contains(prompt, "汉语")
}

// stripContextMarkerLeaks removes the "[当前章节]" / "[前文译文结尾…]" context
// markers that small models sometimes echo into the translation even though
// the prompt forbids it. Known marker+title pairs are removed exactly first;
// a leaked marker followed by a short title token is then stripped
// defensively.
func stripContextMarkerLeaks(s string, req TranslateRequest) string {
	if !strings.Contains(s, "[当前章节") && !strings.Contains(s, "[前文译文结尾") {
		return s
	}
	if req.ChapterTitle != "" {
		s = strings.ReplaceAll(s, "[当前章节] "+req.ChapterTitle, "")
		s = strings.ReplaceAll(s, "[当前章节]"+req.ChapterTitle, "")
	}
	if req.PrevTail != "" {
		s = strings.ReplaceAll(s, "[前文译文结尾·仅供参考，禁止翻译或输出] "+req.PrevTail, "")
		s = strings.ReplaceAll(s, "[前文译文结尾·仅供参考，禁止翻译或输出]"+req.PrevTail, "")
	}
	// Defensive: drop a leading marker plus one following whitespace-delimited
	// token (a translated chapter title), keeping the rest of the paragraph.
	for _, marker := range []string{"[当前章节]", "[前文译文结尾·仅供参考，禁止翻译或输出]"} {
		trimmed := strings.TrimSpace(s)
		if !strings.HasPrefix(trimmed, marker) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(marker):])
		fields := strings.SplitN(rest, " ", 2)
		if len(fields) == 2 && utf8.RuneCountInString(fields[0]) <= 30 {
			s = fields[1]
		} else {
			s = rest
		}
	}
	return strings.TrimSpace(s)
}

func shouldBypassTranslation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "www.") {
		return true
	}

	if !strings.Contains(trimmed, " ") {
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp", ".ico", ".css", ".js", ".json", ".xml", ".woff", ".woff2", ".ttf", ".otf"} {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
	}

	letterCount := 0
	for _, r := range trimmed {
		if unicode.IsLetter(r) {
			letterCount++
		}
	}
	return letterCount == 0
}

func isASCIILowerWord(s string) bool {
	if s == "" {
		return false
	}
	hasLetter := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			hasLetter = true
			continue
		}
		return false
	}
	return hasLetter
}
