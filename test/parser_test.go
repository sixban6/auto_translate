package test

import (
	"archive/zip"
	"io"
	"os"
	"strings"
	"testing"

	"auto_translate/pkg/parser"
)

func TestTxtParser(t *testing.T) {
	inFile := "test_in.txt"
	outFile := "test_out.txt"
	content := "Paragraph 1\n\n     \n\nParagraph 2"
	os.WriteFile(inFile, []byte(content), 0644)
	defer os.Remove(inFile)
	defer os.Remove(outFile)

	p, err := parser.GetParser(".txt")
	if err != nil {
		t.Fatal(err)
	}

	blocks, err := p.Extract(inFile)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	if len(blocks) != 2 {
		t.Fatalf("Expected 2 valid blocks, got %d", len(blocks))
	}
	if blocks[0].OriginalText != "Paragraph 1" || blocks[1].OriginalText != "Paragraph 2" {
		t.Errorf("Unexpected block content")
	}

	// Assemble test
	tBlocks := []parser.TranslatedBlock{
		{ID: blocks[0].ID, TranslatedText: "段落 1"},
		{ID: blocks[1].ID, TranslatedText: "段落 2"},
	}

	err = p.Assemble(tBlocks, outFile, true)
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}

	outBytes, _ := os.ReadFile(outFile)
	outContent := string(outBytes)

	if !strings.Contains(outContent, "Paragraph 1\n段落 1") {
		t.Errorf("Assemble didn't output correct bilingual format, got: %v", outContent)
	}
}

func TestEpubParser(t *testing.T) {
	// Let's create a minimal test epub zip
	testZip := "test_epub.epub"
	f, _ := os.Create(testZip)
	w := zip.NewWriter(f)

	// Create proper mimetype
	m, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	m.Write([]byte("application/epub+zip"))

	// Create html file
	h, _ := w.Create("OEBPS/test.xhtml")
	h.Write([]byte("<html><body><p>Hello World</p><script>var ignore = 'me';</script></body></html>"))
	w.Close()
	f.Close()
	defer os.Remove(testZip)

	outFile := "test_out.epub"
	defer os.Remove(outFile)

	p, err := parser.GetParser(".epub")
	if err != nil {
		t.Fatal(err)
	}

	blocks, err := p.Extract(testZip)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d (might have parsed script?)", len(blocks))
	}
	if blocks[0].OriginalText != "Hello World" {
		t.Errorf("Unexpected extracted text: %s", blocks[0].OriginalText)
	}

	// Assemble
	tBlocks := []parser.TranslatedBlock{
		{ID: blocks[0].ID, TranslatedText: "你好世界"},
	}
	err = p.Assemble(tBlocks, outFile, true)
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}

	// Verify output zip
	r, _ := zip.OpenReader(outFile)
	defer r.Close()

	var newHtml string
	for _, zf := range r.File {
		if zf.Name == "OEBPS/test.xhtml" {
			rc, _ := zf.Open()
			buf, _ := io.ReadAll(rc)
			newHtml = string(buf)
			rc.Close()
		}
	}

	if !strings.Contains(newHtml, "你好世界") {
		t.Errorf("Output HTML missing translation: %s", newHtml)
	}
	if !strings.Contains(newHtml, "Hello World") {
		t.Errorf("Output HTML missing original in bilingual mode: %s", newHtml)
	}
	if strings.Index(newHtml, "Hello World") > strings.Index(newHtml, "你好世界") {
		t.Errorf("Output HTML bilingual order should be original first then translation: %s", newHtml)
	}
	if !strings.Contains(newHtml, "var ignore") {
		t.Errorf("Output HTML lost script tag: %s", newHtml)
	}
}
