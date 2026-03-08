package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration(t *testing.T) {
	// Build the CLI binary
	cmd := exec.Command("go", "build", "-o", "autotrans_test_bin", "../cmd/autotrans")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build CLI: %v", err)
	}
	defer os.Remove("autotrans_test_bin")

	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "Mock Translation",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer server.Close()

	// Setup input and config
	inputPath := "integration_in.txt"
	outputPath := "integration_out.txt"
	configPath := "integration_config.json"
	defer os.Remove(inputPath)
	defer os.Remove(outputPath)
	defer os.Remove(configPath)

	os.WriteFile(inputPath, []byte("Hello World.\n\nGreat Integration."), 0644)

	configStr := `{
		"api_url": "` + server.URL + `",
		"model": "test",
		"prompt": "test",
		"input_file": "` + inputPath + `",
		"output_file": "` + outputPath + `",
		"bilingual": true
	}`
	os.WriteFile(configPath, []byte(configStr), 0644)

	// Run the binary
	absPath, _ := filepath.Abs("autotrans_test_bin")
	runCmd := exec.Command(absPath, "-c", configPath)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI execution failed: %v\nOutput: %s", err, string(out))
	}

	// Verify output
	resultBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	resultStr := string(resultBytes)
	if !strings.Contains(resultStr, "Mock Translation") {
		t.Errorf("Output did not contain translation. Got %s", resultStr)
	}
	if !strings.Contains(resultStr, "Great Integration") {
		t.Errorf("Output did not contain original in bilingual mode. Got %s", resultStr)
	}
	if strings.Index(resultStr, "Hello World.") > strings.Index(resultStr, "Mock Translation") {
		t.Errorf("Expected bilingual order to keep original above translation. Got %s", resultStr)
	}
}
