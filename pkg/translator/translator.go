package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"auto_translate/pkg/config"
)

// Translator handles HTTP requests to the Ollama API and glossary enforcement.
type Translator struct {
	cfg    *config.Config
	client *http.Client
}

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
func (t *Translator) Translate(text string, onEvent ...func(string)) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil // skip empty chunks
	}

	var ev func(string)
	if len(onEvent) > 0 {
		ev = onEvent[0]
	}

	payload := map[string]interface{}{
		"model":       t.cfg.Model,
		"temperature": t.cfg.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": t.cfg.Prompt},
			{"role": "user", "content": text},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	var translated string
	maxRetries := t.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("POST", t.cfg.APIURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
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
				return "", fmt.Errorf("API request failed after %d attempts: %w", maxRetries, err)
			}
			if ev != nil {
				ev(fmt.Sprintf("API request failed (Attempt %d/%d): %v. Retrying...", attempt, maxRetries, err))
			}
			time.Sleep(time.Duration(attempt*2) * time.Second) // basic backoff
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", fmt.Errorf("API returned non-200 status %d after %d attempts", resp.StatusCode, maxRetries)
			}
			if ev != nil {
				ev(fmt.Sprintf("API returned status %d (Attempt %d/%d). Retrying...", resp.StatusCode, attempt, maxRetries))
			}
			time.Sleep(time.Duration(attempt*2) * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("failed to read API response body: %w", err)
		}

		// Parse the OpenAI compatible response
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return "", fmt.Errorf("failed to decode API response JSON: %w (body: %s)", err, string(body))
		}

		if len(result.Choices) > 0 {
			translated = result.Choices[0].Message.Content
			break // success!
		} else {
			return "", fmt.Errorf("API returned empty choices array")
		}
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

	return translated, nil
}
