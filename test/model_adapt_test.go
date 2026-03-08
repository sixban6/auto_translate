package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

func TestTranslator_ModelAdaptationPayload(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		expectThinkAbsent bool
		expectPath        string
	}{
		{
			name:              "non translategemma should set think false",
			model:             "qwen2.5:14b",
			expectThinkAbsent: false,
			expectPath:        "/api/chat",
		},
		{
			name:              "translategemma should not set think",
			model:             "translategemma:2b",
			expectThinkAbsent: true,
			expectPath:        "/",
		},
		{
			name:              "translategemma with uppercase should not set think",
			model:             "TranslateGemma:27b",
			expectThinkAbsent: true,
			expectPath:        "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.expectPath {
					t.Fatalf("unexpected request path: %s", r.URL.Path)
				}

				var payload map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode payload failed: %v", err)
				}

				streamVal, ok := payload["stream"]
				if !ok {
					t.Fatalf("payload missing stream field")
				}
				streamBool, ok := streamVal.(bool)
				if !ok || streamBool {
					t.Fatalf("payload stream expected false, got %#v", streamVal)
				}

				thinkVal, thinkExists := payload["think"]
				if tt.expectThinkAbsent {
					if thinkExists {
						t.Fatalf("payload think should be absent, got %#v", thinkVal)
					}
				} else {
					if !thinkExists {
						t.Fatalf("payload think should exist")
					}
					thinkBool, ok := thinkVal.(bool)
					if !ok || thinkBool {
						t.Fatalf("payload think expected false, got %#v", thinkVal)
					}
				}

				w.Header().Set("Content-Type", "application/json")
				if tt.expectThinkAbsent {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"mock translation"}}]}`))
				} else {
					_, _ = w.Write([]byte(`{"message":{"content":"mock translation"}}`))
				}
			}))
			defer server.Close()

			cfg := &config.Config{
				APIURL:            server.URL,
				Model:             tt.model,
				Prompt:            "test prompt",
				Temperature:       0.1,
				RequestTimeoutSec: 1,
				MaxRetries:        1,
			}
			tr := translator.New(cfg)

			got, status, err := tr.Translate("test content")
			if err != nil {
				t.Fatalf("Translate failed: %v", err)
			}
			if status != translator.StatusSuccess {
				t.Fatalf("unexpected status: %v", status)
			}
			if got != "mock translation" {
				t.Fatalf("unexpected translation: %q", got)
			}
		})
	}
}
