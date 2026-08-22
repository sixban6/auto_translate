package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

// talebPara is a real oversized single paragraph (~1900 runes) that exceeds
// MaxChunkSize and therefore gets sentence-split into several piece batches.
const talebPara = `I said that we can estimate, even measure, fragility and antifragility, while we cannot calculate risks and probabilities of shocks and rare events, no matter how sophisticated we get. Risk management as practiced is the study of an event taking place in the future, and only some economists and other lunatics can claim—against experience—to "measure" the future incidence of these rare events, with suckers listening to them—against experience and the track record of such claims. But fragility and antifragility are part of the current property of an object, a coffee table, a company, an industry, a country, a political system. We can detect fragility, see it, even in many cases measure it, or at least measure comparative fragility with a small error while comparisons of risk have been (so far) unreliable. You cannot say with any reliability that a certain remote event or shock is more likely than another (unless you enjoy deceiving yourself), but you can state with a lot more confidence that an object or a structure is more fragile than another should a certain event happen. You can easily tell that your grandmother is more fragile to abrupt changes in temperature than you, that some military dictatorship is more fragile than Switzerland should political change happen, that a bank is more fragile than another should a crisis occur, or that a poorly built modern building is more fragile than the Cathedral of Chartres should an earthquake happen. And—centrally—you can even make the prediction of which one will last longer.`

const shortPara = "Fragility is measurable. That is the central claim."

func writeTxtFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// mockEngine returns "[T]"+input for texts containing marker, and echoes the
// input unchanged otherwise (simulating a model that failed to translate).
func mockEngine(t *testing.T, translateMarker string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			}
		}
		content := text
		if translateMarker != "" && strings.Contains(text, translateMarker) {
			content = "[T]" + text
		} else if translateMarker != "" {
			content = text // echo: model failed to translate this piece
		} else {
			content = text // always echo
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": content}},
			},
		})
	}))
}

func splitTestConfig(serverURL string) *config.Config {
	return &config.Config{
		APIURL:            serverURL,
		Model:             "translategemma:12b",
		MaxChunkSize:      400, // forces the ~1900-rune paragraph to split
		Concurrency:       1,
		Temperature:       0,
		RequestTimeoutSec: 5,
		MaxRetries:        1,
	}
}

func runBilingualTxt(t *testing.T, cfg *config.Config, inputPath string) string {
	t.Helper()
	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)
	p, err := parser.GetParser(".txt")
	if err != nil {
		t.Fatalf("parser: %v", err)
	}
	blocks, err := p.Extract(inputPath)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	translated, _, err := proc.Process(context.Background(), blocks, nil, nil, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := p.Assemble(translated, outPath, true); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(out)
}

// TestSplitBlock_AllPiecesEcho_NoDuplication reproduces the reported bug:
// an oversized paragraph is split into pieces, the model echoes every piece
// (fallback), and the bilingual output must keep the paragraph exactly once
// instead of appending the lossily re-joined English echo after the original.
func TestSplitBlock_AllPiecesEcho_NoDuplication(t *testing.T) {
	server := mockEngine(t, "") // always echo
	defer server.Close()
	cfg := splitTestConfig(server.URL)

	input := writeTxtFixture(t, talebPara+"\n\n"+shortPara)
	out := runBilingualTxt(t, cfg, input)

	// The original paragraph must appear exactly once — not again as a
	// re-joined echo below itself.
	if n := strings.Count(out, "Risk management as practiced"); n != 1 {
		t.Errorf("oversized paragraph duplicated: found %d occurrences of a distinctive sentence, want 1", n)
	}
	// The corrupted no-space re-join signature must not appear at all.
	if strings.Contains(out, "get.Risk") {
		t.Errorf("lossy piece re-join leaked into output (\"get.Risk\")")
	}
	// The short paragraph is echoed as well and must also stay single.
	if n := strings.Count(out, "Fragility is measurable"); n != 1 {
		t.Errorf("short paragraph duplicated: %d occurrences, want 1", n)
	}
}

// TestSplitBlock_MixedPieces verifies a partially translated split block:
// translated pieces are joined with proper separators and echoed pieces keep
// their sentence boundaries, without duplicating the whole source paragraph.
func TestSplitBlock_MixedPieces(t *testing.T) {
	// Translate only the piece that starts the paragraph ("I said that...");
	// every later piece is echoed back untranslated.
	server := mockEngine(t, "I said that")
	defer server.Close()
	cfg := splitTestConfig(server.URL)

	input := writeTxtFixture(t, talebPara+"\n\n"+shortPara)
	out := runBilingualTxt(t, cfg, input)

	if !strings.Contains(out, "[T]") {
		t.Errorf("expected translated pieces to carry the [T] marker")
	}
	// Corrupted join signature must not appear.
	if strings.Contains(out, "get.Risk") {
		t.Errorf("lossy piece re-join leaked into output")
	}
	// The injected translation line exists exactly once after the original.
	if n := strings.Count(out, "[T]I said that"); n != 1 {
		t.Errorf("expected exactly one injected translation line, got %d", n)
	}
}

// TestSplitBlock_LegacyWholeState_NoDuplication covers resume with a legacy
// checkpoint from a run that translated the oversized block as one unit
// (key "{block}-0" holding the WHOLE translation). The reassembled block
// must be exactly that translation — not whole + re-split pieces.
func TestSplitBlock_LegacyWholeState_NoDuplication(t *testing.T) {
	cfg := &config.Config{MaxChunkSize: 400}
	proc := processor.New(cfg, nil)

	whole := "这是整段的旧版译文，来自拆分机制引入之前的运行。"
	blocks := []parser.TextBlock{{ID: "txt_0", OriginalText: talebPara}}
	state := map[string]string{"txt_0-0": whole}

	out := proc.Reassemble(blocks, state)
	if len(out) != 1 {
		t.Fatalf("expected 1 block, got %d", len(out))
	}
	if out[0].TranslatedText != whole {
		t.Errorf("legacy whole-block translation corrupted:\n%q", out[0].TranslatedText)
	}
	if n := strings.Count(out[0].TranslatedText, whole); n != 1 {
		t.Errorf("whole translation appears %d times, want 1", n)
	}
}

// TestSplitBlock_LegacyPartialState_Dropped verifies that a partial legacy
// checkpoint whose piece boundaries no longer match the current split is
// discarded instead of splicing stale pieces into the block.
func TestSplitBlock_LegacyPartialState_Dropped(t *testing.T) {
	cfg := &config.Config{MaxChunkSize: 400}
	proc := processor.New(cfg, nil)

	blocks := []parser.TextBlock{{ID: "txt_0", OriginalText: talebPara}}
	// Stale: covers only pieces 0 and 1 of an old, different split.
	state := map[string]string{"txt_0-0": "旧段零", "txt_0-1": "旧段一"}

	out := proc.Reassemble(blocks, state)
	if len(out) != 1 {
		t.Fatalf("expected 1 block, got %d", len(out))
	}
	// The misaligned legacy must not leak into the block.
	if strings.Contains(out[0].TranslatedText, "旧段零") || strings.Contains(out[0].TranslatedText, "旧段一") {
		t.Errorf("stale legacy pieces leaked into output: %q", preview(out[0].TranslatedText))
	}
	// Without any usable checkpoint the original is preserved verbatim.
	if strings.TrimSpace(out[0].TranslatedText) != strings.TrimSpace(talebPara) {
		t.Errorf("expected verbatim original fallback")
	}
}

func preview(s string) string {
	if len(s) > 80 {
		return fmt.Sprintf("%s...", s[:80])
	}
	return s
}
