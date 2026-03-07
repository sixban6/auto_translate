package test

import (
	"archive/zip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationEpubSample(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", "autotrans_test_bin_epub", "../cmd/autotrans")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build CLI: %v", err)
	}
	defer os.Remove("autotrans_test_bin_epub")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)

		text := ""
		for _, m := range payload.Messages {
			if m.Role == "user" {
				text = m.Content
				break
			}
		}

		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "MOCK:" + text,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	inputPath := "sample_en.epub"
	outputPath := "integration_epub_out.epub"
	configPath := "integration_epub_config.json"
	defer os.Remove(outputPath)
	defer os.Remove(configPath)

	configStr := `{
		"api_url": "` + server.URL + `",
		"model": "test-model",
		"prompt": "test prompt",
		"input_file": "` + inputPath + `",
		"output_file": "` + outputPath + `",
		"bilingual": true
	}`
	os.WriteFile(configPath, []byte(configStr), 0644)

	absPath, _ := filepath.Abs("autotrans_test_bin_epub")
	runCmd := exec.Command(absPath, "-c", configPath)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI execution failed: %v\nOutput: %s", err, string(out))
	}

	r, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatalf("Failed to open output epub: %v", err)
	}
	defer r.Close()

	var htmlContent string
	for _, zf := range r.File {
		if zf.Name == "OEBPS/chapter1.xhtml" {
			rc, _ := zf.Open()
			buf, _ := io.ReadAll(rc)
			rc.Close()
			htmlContent = string(buf)
			break
		}
	}

	if htmlContent == "" {
		t.Fatalf("Output epub missing chapter1.xhtml")
	}

	expectations := []string{
		"MOCK:Chapter 1",
		"Chapter 1",
		"MOCK:The quick brown fox jumps over the lazy dog.",
		"The quick brown fox jumps over the lazy dog.",
		"MOCK:Volume Spread Analysis focuses on the relationship between price and volume.",
		"Volume Spread Analysis focuses on the relationship between price and volume.",
		"MOCK:Wyckoff Theory explains market phases and the role of smart money.",
		"Wyckoff Theory explains market phases and the role of smart money.",
	}

	for _, expected := range expectations {
		if !strings.Contains(htmlContent, expected) {
			t.Fatalf("Output missing expected content: %s", expected)
		}
	}
}
