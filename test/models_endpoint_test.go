package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"auto_translate/pkg/config"
)

func TestModelsEndpoint_CustomURL(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	// OpenAI-style server (what mlx_lm.server exposes).
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"Qwen3.8-27B-test"},{"id":"Hy-MT2-1.8B"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer openaiSrv.Close()

	resp, err := http.Get(srv.BaseURL + "/api/models?api_url=" + openaiSrv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Models []struct {
			Name      string `json:"name"`
			Engine    string `json:"engine"`
			ChunkSize int    `json:"chunk_size"`
		} `json:"models"`
		DetectedEngine string `json:"detected_engine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(payload.Models) != 2 {
		t.Fatalf("unexpected models: %+v", payload.Models)
	}
	if payload.Models[0].Engine != "mlx" {
		t.Errorf("expected mlx engine tag, got %q", payload.Models[0].Engine)
	}
	if payload.DetectedEngine != "mlx" {
		t.Errorf("expected detected engine mlx, got %q", payload.DetectedEngine)
	}
	// Recommended chunk size scales with model size (sorted: 1.8B first).
	byName := map[string]int{}
	for _, m := range payload.Models {
		byName[m.Name] = m.ChunkSize
	}
	if byName["Qwen3.8-27B-test"] != 3200 {
		t.Errorf("expected 3200 for 27B model, got %d", byName["Qwen3.8-27B-test"])
	}
	if byName["Hy-MT2-1.8B"] != 2400 {
		t.Errorf("expected 2400 for small model, got %d", byName["Hy-MT2-1.8B"])
	}

	// Ollama-style server answering only the native /api/tags endpoint.
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"qwen2.5:14b"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaSrv.Close()

	resp2, err := http.Get(srv.BaseURL + "/api/models?api_url=" + ollamaSrv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()
	var payload2 struct {
		Models []struct {
			Name   string `json:"name"`
			Engine string `json:"engine"`
		} `json:"models"`
		DetectedEngine string `json:"detected_engine"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&payload2); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(payload2.Models) != 1 || payload2.Models[0].Name != "qwen2.5:14b" {
		t.Fatalf("unexpected models: %+v", payload2.Models)
	}
	if payload2.Models[0].Engine != "ollama" {
		t.Errorf("expected ollama engine tag, got %q", payload2.Models[0].Engine)
	}
	if payload2.DetectedEngine != "ollama" {
		t.Errorf("expected detected engine ollama, got %q", payload2.DetectedEngine)
	}
}

func TestModelsEndpoint_AutoDetectShape(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	// No api_url: the backend probes the default local engine endpoints
	// (127.0.0.1:8080 / 127.0.0.1:11434). They may or may not be live on the
	// machine running the tests, so only validate response consistency.
	resp, err := http.Get(srv.BaseURL + "/api/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Models []struct {
			Name   string `json:"name"`
			Engine string `json:"engine"`
		} `json:"models"`
		DetectedEngine string `json:"detected_engine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(payload.Models) > 0 {
		if payload.DetectedEngine == "" {
			t.Errorf("detected_engine must be set when models exist")
		}
		for _, m := range payload.Models {
			if m.Name == "" || (m.Engine != "mlx" && m.Engine != "ollama" && m.Engine != "omlx") {
				t.Errorf("invalid model entry: %+v", m)
			}
		}
	} else if payload.DetectedEngine != "" {
		t.Errorf("detected_engine must be empty when no models found, got %q", payload.DetectedEngine)
	}
}

func TestModelsEndpoint_OmlxAuthRetry(t *testing.T) {
	key := config.ReadOMLXAPIKey()
	if key == "" {
		t.Skip("no local oMLX api key (~/.omlx/settings.json) for auth retry test")
	}

	// oMLX-style server: /v1/models requires the bearer key.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"API key required"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"Qwen3.8-27B-oQ4e-mtp"}]}`))
	}))
	defer srv.Close()

	webrunner := startServer(t)
	defer webrunner.Close()

	resp, err := http.Get(webrunner.BaseURL + "/api/models?api_url=" + srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Models []struct {
			Name   string `json:"name"`
			Engine string `json:"engine"`
		} `json:"models"`
		DetectedEngine string `json:"detected_engine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(payload.Models) != 1 || payload.Models[0].Name != "Qwen3.8-27B-oQ4e-mtp" {
		t.Fatalf("auth retry did not yield models: %+v", payload.Models)
	}
}
