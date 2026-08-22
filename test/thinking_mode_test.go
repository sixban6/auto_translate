package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

func thinkingCfg(serverURL string) *config.Config {
	return &config.Config{
		APIURL:            serverURL,
		Engine:            "omlx",
		Model:             "Qwen3.8-27B-oQ4e-mtp",
		Prompt:            "Translate English to Chinese.",
		Temperature:       0.1,
		RequestTimeoutSec: 5,
		MaxRetries:        1,
	}
}

// TestRequest_DisablesThinking verifies the OpenAI-compatible payload asks
// the server to turn thinking off (Qwen3-style models default to ON).
func TestRequest_DisablesThinking(t *testing.T) {
	var mu sync.Mutex
	var gotKwargs map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		if kwargs, ok := payload["chat_template_kwargs"].(map[string]interface{}); ok {
			mu.Lock()
			gotKwargs = kwargs
			mu.Unlock()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "好的译文"}},
			},
		})
	}))
	defer server.Close()

	tr := translator.New(thinkingCfg(server.URL))
	_, status, err := tr.TranslateBatch(context.Background(), translator.TranslateRequest{Text: "Hello world"}, nil)
	if err != nil || status != translator.StatusSuccess {
		t.Fatalf("translate failed: status=%v err=%v", status, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotKwargs == nil {
		t.Fatalf("request did not carry chat_template_kwargs")
	}
	if v, _ := gotKwargs["enable_thinking"].(bool); !v == false {
		t.Errorf("enable_thinking = %v, want false", gotKwargs["enable_thinking"])
	}
}

// TestResponse_ThinkingBlocksStripped verifies thinking content that leaks
// into message.content is removed before the translation is used.
func TestResponse_ThinkingBlocksStripped(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "complete think block",
			response: "<think>用户要翻译，我先分析一下语法结构。</think>\n你好，世界。",
			want:     "你好，世界。",
		},
		{
			name:     "unterminated think block",
			response: "<think>让我想想这句话怎么翻译比较好，首先",
			want:     "",
		},
		{
			name:     "thinking variant tag",
			response: "<thinking>reasoning here</thinking>译文正文",
			want:     "译文正文",
		},
		{
			name:     "no thinking at all",
			response: "普通译文",
			want:     "普通译文",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"choices": []map[string]interface{}{
						{"message": map[string]string{"content": tc.response}},
					},
				})
			}))
			defer server.Close()

			tr := translator.New(thinkingCfg(server.URL))
			got, _, _ := tr.TranslateBatch(context.Background(), translator.TranslateRequest{Text: "Hello world"}, nil)
			if strings.TrimSpace(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStripThinkingBlocks unit-tests the exported helper directly.
func TestStripThinkingBlocks(t *testing.T) {
	if got := translator.StripThinkingBlocks("<think>a</think>\nb"); got != "b" {
		t.Errorf("complete block: got %q", got)
	}
	if got := translator.StripThinkingBlocks("<think>unterminated"); got != "" {
		t.Errorf("unterminated block: got %q", got)
	}
	if got := translator.StripThinkingBlocks("plain"); got != "plain" {
		t.Errorf("plain text mangled: got %q", got)
	}
}
