package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/translator"
)

// TestOmlxPayload_PinsSampler verifies the oMLX request pins the sampler
// (min_p=0.0 etc.) to bypass the server-side corrupted default that silently
// empties responses.
func TestOmlxPayload_PinsSampler(t *testing.T) {
	var mu = make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		json.NewDecoder(r.Body).Decode(&p)
		mu <- p
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "译文"}},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "Hy-MT2-7B-4bit",
		Temperature: 0.1, RequestTimeoutSec: 5, MaxRetries: 1,
	}
	if _, _, err := translator.New(cfg).TranslateBatch(context.Background(),
		translator.TranslateRequest{Text: "Hello world test"}, nil); err != nil {
		t.Fatalf("translate: %v", err)
	}
	p := <-mu
	if v, _ := p["min_p"].(float64); v != 0.0 {
		t.Errorf("min_p = %v, want 0.0", p["min_p"])
	}
	if v, _ := p["top_p"].(float64); v != 0.6 {
		t.Errorf("top_p = %v, want 0.6", p["top_p"])
	}
	if v, _ := p["top_k"].(float64); v != 20 {
		t.Errorf("top_k = %v, want 20", p["top_k"])
	}
	if v, _ := p["repetition_penalty"].(float64); v != 1.05 {
		t.Errorf("repetition_penalty = %v, want 1.05", p["repetition_penalty"])
	}
	kwargs, ok := p["chat_template_kwargs"].(map[string]interface{})
	if !ok || kwargs["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs missing/wrong: %v", p["chat_template_kwargs"])
	}
}

// TestKwargsDowngradeOnRejection verifies a 4xx caused by the non-standard
// chat_template_kwargs field disables it and retries successfully.
func TestKwargsDowngradeOnRejection(t *testing.T) {
	var mu = make(chan bool, 4)
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		var p map[string]interface{}
		json.NewDecoder(r.Body).Decode(&p)
		_, has := p["chat_template_kwargs"]
		mu <- has
		if call == 1 {
			w.WriteHeader(http.StatusBadRequest) // reject the unknown field
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "译文"}},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		APIURL: server.URL, Engine: "omlx", Model: "m",
		Temperature: 0.1, RequestTimeoutSec: 5, MaxRetries: 3,
	}
	got, status, err := translator.New(cfg).TranslateBatch(context.Background(),
		translator.TranslateRequest{Text: "Hello world test"}, nil)
	if err != nil || status != translator.StatusSuccess {
		t.Fatalf("downgrade retry failed: status=%v err=%v", status, err)
	}
	if got != "译文" {
		t.Errorf("got %q", got)
	}
	first := <-mu
	second := <-mu
	if !first {
		t.Errorf("first request should carry chat_template_kwargs")
	}
	if second {
		t.Errorf("second request must drop chat_template_kwargs")
	}
}
