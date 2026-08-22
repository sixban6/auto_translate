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
		// The input has two paragraphs sharing one chapter batch; reply with
		// two translated paragraphs to preserve the mapping.
		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "译文甲\n\n译文乙",
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
	if !strings.Contains(resultStr, "译文甲") || !strings.Contains(resultStr, "译文乙") {
		t.Errorf("Output did not contain translations. Got %s", resultStr)
	}
	if !strings.Contains(resultStr, "Great Integration") {
		t.Errorf("Output did not contain original in bilingual mode. Got %s", resultStr)
	}
	if strings.Index(resultStr, "Hello World.") > strings.Index(resultStr, "译文甲") {
		t.Errorf("Expected bilingual order to keep original above translation. Got %s", resultStr)
	}
	if strings.Index(resultStr, "Great Integration") > strings.Index(resultStr, "译文乙") {
		t.Errorf("Expected bilingual order to keep original above translation. Got %s", resultStr)
	}
}
