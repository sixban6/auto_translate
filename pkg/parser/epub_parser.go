package parser

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type EpubParser struct {
	originalZipPath string
	parsedFiles     map[string]*html.Node
	textNodes       map[string]*html.Node
}

// NewEpubParser returns a parser capable of extracting and rebuilding EPUB files.
func NewEpubParser() *EpubParser {
	return &EpubParser{
		parsedFiles: make(map[string]*html.Node),
		textNodes:   make(map[string]*html.Node),
	}
}

// Extract extracts translatable text nodes fromxhtml/html files inside the epub.
func (p *EpubParser) Extract(inputPath string) ([]TextBlock, error) {
	p.originalZipPath = inputPath
	r, err := zip.OpenReader(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open epub: %w", err)
	}
	defer r.Close()

	var blocks []TextBlock

	for _, f := range r.File {
		name := f.Name
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".xhtml") || strings.HasSuffix(lower, ".htm") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			doc, err := html.Parse(rc)
			rc.Close()
			if err != nil {
				continue
			}

			p.parsedFiles[name] = doc

			// Traverse to find text nodes
			var traverse func(*html.Node)
			nodeIndex := 0
			traverse = func(n *html.Node) {
				if n.Type == html.TextNode {
					text := strings.TrimSpace(n.Data)
					// Verify this is not a script or style child
					if n.Parent != nil && (n.Parent.DataAtom == atom.Script || n.Parent.DataAtom == atom.Style) {
						return
					}
					if text != "" {
						// Store it
						id := fmt.Sprintf("%s_node_%d", name, nodeIndex)
						nodeIndex++
						p.textNodes[id] = n

						blocks = append(blocks, TextBlock{
							ID:           id,
							OriginalText: text,
						})
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
			}
			traverse(doc)
		}
	}

	return blocks, nil
}

// Assemble rebuilds the EPUB using original non-html files and rendered modified html files.
func (p *EpubParser) Assemble(blocks []TranslatedBlock, outputPath string, bilingual bool) error {
	if p.originalZipPath == "" {
		return fmt.Errorf("Extract() must be called before Assemble()")
	}

	// Update the AST with translations
	transMap := make(map[string]string)
	for _, b := range blocks {
		transMap[b.ID] = b.TranslatedText
	}

	for id, tNode := range p.textNodes {
		translated, ok := transMap[id]
		if !ok || strings.TrimSpace(translated) == "" {
			continue // skip empty
		}
		if strings.TrimSpace(translated) == "<!--merged-->" {
			tNode.Data = ""
			continue
		}

		if bilingual {
			translatedNode := &html.Node{Type: html.TextNode, Data: translated}
			brNode := &html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"}
			origNode := &html.Node{Type: html.TextNode, Data: tNode.Data}

			parent := tNode.Parent
			if parent != nil {
				parent.InsertBefore(origNode, tNode)
				parent.InsertBefore(brNode, tNode)
				parent.InsertBefore(translatedNode, tNode)
				parent.RemoveChild(tNode)
			} else {
				tNode.Data = fmt.Sprintf("%s / %s", tNode.Data, translated)
			}
		} else {
			tNode.Data = translated
		}
	}

	// Write out to new zip
	originalZip, err := zip.OpenReader(p.originalZipPath)
	if err != nil {
		return err
	}
	defer originalZip.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	w := zip.NewWriter(outFile)
	defer w.Close()

	for _, f := range originalZip.File {
		// Create file signature in the new zip
		// We copy compression flags but if we render html we should just deflate
		header := f.FileHeader
		if f.Name == "mimetype" {
			header.Method = zip.Store // must not be compressed
		} else if header.Method == zip.Store {
			header.Method = zip.Deflate // force compress anything else
		}

		fw, err := w.CreateHeader(&header)
		if err != nil {
			return err
		}

		// check if it's one of the files we parsed and modified
		if doc, ok := p.parsedFiles[f.Name]; ok {
			html.Render(fw, doc)
			continue
		}

		// Otherwise just copy raw data
		rc, err := f.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}

	return nil
}
