package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

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

// Translator handles HTTP requests to the Ollama API and glossary enforcement.
type Translator struct {
	cfg    *config.Config
	client *http.Client
}

var latinDoubleDashPattern = regexp.MustCompile(`([A-Za-z])[—–-]{2,}([A-Za-z])`)
var rePrefixBeforeHanPattern = regexp.MustCompile(`(?i)\bre\s*[—–-]?\s*([\p{Han}])`)
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

// Translate attempts to translate a given text snippet via the API.
// Implements retries and handles glossary mapping.
func (t *Translator) Translate(ctx context.Context, text string, onEvent ...func(string)) (string, TranslationStatus, error) {
	if strings.TrimSpace(text) == "" {
		return "", StatusSkip, nil // skip empty chunks
	}
	if shouldBypassTranslation(text) {
		return text, StatusFallback, nil
	}

	var ev func(string)
	if len(onEvent) > 0 {
		ev = onEvent[0]
	}

	// 0. Short Text / Glossary Fallback Strategy
	textTrimmed := strings.TrimSpace(text)
	runes := []rune(textTrimmed)
	if len(runes) < 20 {
		// Priority 1: Check Glossary for exact match
		for en, cn := range t.cfg.Glossary {
			if strings.EqualFold(textTrimmed, strings.TrimSpace(en)) {
				return cn, StatusFallback, nil
			}
		}
		// Priority 2: If extremely short and no spaces, return as-is
		if len(runes) < 5 && !strings.Contains(textTrimmed, " ") {
			if !isASCIILowerWord(textTrimmed) {
				return text, StatusFallback, nil
			}
		}
	}

	requestURL := t.cfg.APIURL
	payload := map[string]interface{}{
		"model":       t.cfg.Model,
		"temperature": t.cfg.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": t.cfg.Prompt},
			{"role": "user", "content": text},
		},
		"stream": false,
	}
	if !strings.Contains(strings.ToLower(t.cfg.Model), "translategemma") {
		requestURL = toOllamaChatURL(t.cfg.APIURL)
		payload = map[string]interface{}{
			"model": t.cfg.Model,
			"messages": []map[string]string{
				{"role": "system", "content": t.cfg.Prompt},
				{"role": "user", "content": text},
			},
			"stream":  false,
			"think":   false,
			"options": map[string]interface{}{"temperature": t.cfg.Temperature},
		}
	}

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
		req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return "", StatusFailed, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

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

		resp, err := t.client.Do(req)
		close(doneCh)

		if err != nil {
			if attempt == maxRetries {
				return "", StatusFailed, fmt.Errorf("API request failed after %d attempts: %w", maxRetries, err)
			}
			if ev != nil {
				ev(fmt.Sprintf("API request failed (Attempt %d/%d): %v. Retrying...", attempt, maxRetries, err))
			}
			time.Sleep(time.Duration(attempt*8) * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", StatusFailed, fmt.Errorf("API returned non-200 status %d after %d attempts", resp.StatusCode, maxRetries)
			}
			if ev != nil {
				ev(fmt.Sprintf("API returned status %d (Attempt %d/%d). Retrying...", resp.StatusCode, attempt, maxRetries))
			}
			time.Sleep(time.Duration(attempt*8) * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", StatusFailed, fmt.Errorf("failed to read API response body: %w", err)
		}

		// Parse response
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

		if strings.Contains(translated, "请提供需要翻译的文本") ||
			strings.Contains(translated, "无法翻译") ||
			strings.Contains(translated, "未提供上下文") ||
			strings.Contains(translated, "没有任何内容") ||
			strings.Contains(translated, "请提供包含") {
			return text, StatusRefused, fmt.Errorf("model refused to translate (fallback to original): %s", translated)
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

	// 2. Glossary Enforcement
	for en, cn := range t.cfg.Glossary {
		// Only replace if the source text actually contains the term (case-insensitive check)
		if strings.Contains(strings.ToLower(text), strings.ToLower(en)) {
			translated = strings.ReplaceAll(translated, cn, cn) // This is a no-op fallback

			// If we want forceful exact matching across variations, we'd need smart regex,
			// but for now, we trust the model mostly got it right, and we just ensure
			// if the model generated a similar but slightly wrong Chinese term, we don't
			// easily overwrite.
			// Actually, the most naive (but effective) glossary enforcement for OLLAMA is:
			// If the term was in the prompt, Ollie usually follows it. If not, and we have
			// a glossary, we could do regex replaces on expected wrong translations, but
			// we don't know the wrong translations.
			// So, the prompt is our primary defense. We will leave this simple.
			// Currently, just printing missing terms is helpful for debugging.
			if !strings.Contains(translated, cn) {
				// We COULD force append or substitute, but it often breaks grammar.
				// We rely on the System Prompt to enforce it.
			}
		}
	}

	return translated, StatusSuccess, nil
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
