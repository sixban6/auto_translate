package parser

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type EpubParser struct {
	originalZipPath string
	parsedFiles     map[string]*html.Node
	blockInfos      map[string]*blockInfo
}

type nodeTextInfo struct {
	node       *html.Node
	original   string
	leadingWS  string
	trailingWS string
	core       string
}

type blockInfo struct {
	element *html.Node
	nodes   []nodeTextInfo
}

func preserveEdgeWhitespace(original, translated string) string {
	if translated == "" {
		return translated
	}
	prefix := ""
	for _, r := range original {
		if !unicode.IsSpace(r) {
			break
		}
		prefix += string(r)
	}

	suffix := ""
	runes := []rune(original)
	for i := len(runes) - 1; i >= 0; i-- {
		if !unicode.IsSpace(runes[i]) {
			break
		}
		suffix = string(runes[i]) + suffix
	}
	return prefix + strings.TrimSpace(translated) + suffix
}

// NewEpubParser returns a parser capable of extracting and rebuilding EPUB files.
func NewEpubParser() *EpubParser {
	return &EpubParser{
		parsedFiles: make(map[string]*html.Node),
		blockInfos:  make(map[string]*blockInfo),
	}
}

func isBlockElement(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.P, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Li, atom.Blockquote, atom.Figcaption, atom.Caption, atom.Div,
		atom.Section, atom.Article, atom.Pre, atom.Dd, atom.Dt, atom.Th, atom.Td:
		return true
	default:
		return false
	}
}

func splitWhitespaceParts(s string) (string, string, string) {
	if s == "" {
		return "", "", ""
	}
	runes := []rune(s)
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	end := len(runes)
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	leading := string(runes[:start])
	trailing := string(runes[end:])
	core := string(runes[start:end])
	return leading, trailing, core
}

func isAsciiAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func shouldInsertSpace(prevCore, currCore string, prevTrailingWS, currLeadingWS string) bool {
	if prevCore == "" || currCore == "" {
		return false
	}
	if prevTrailingWS != "" || currLeadingWS != "" {
		return true
	}
	prevRunes := []rune(prevCore)
	currRunes := []rune(currCore)
	if len(prevRunes) == 0 || len(currRunes) == 0 {
		return false
	}
	return isAsciiAlnum(prevRunes[len(prevRunes)-1]) && isAsciiAlnum(currRunes[0])
}

func endsWithDash(s string) bool {
	return strings.HasSuffix(s, "-") || strings.HasSuffix(s, "–") || strings.HasSuffix(s, "—")
}

func startsWithLatinLetter(s string) bool {
	if s == "" {
		return false
	}
	r := rune(s[0])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func trimTrailingDash(s string) string {
	return strings.TrimRight(s, "-–—")
}

func collectTextNodesForBlock(n *html.Node, nodes *[]*html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && isBlockElement(c) {
			continue
		}
		if c.Type == html.TextNode {
			if c.Parent != nil && (c.Parent.DataAtom == atom.Script || c.Parent.DataAtom == atom.Style) {
				continue
			}
			if strings.TrimSpace(c.Data) != "" {
				*nodes = append(*nodes, c)
			}
		}
		collectTextNodesForBlock(c, nodes)
	}
}

func buildBlockText(nodes []nodeTextInfo) string {
	var sb strings.Builder
	prevCore := ""
	prevTrailingWS := ""
	for _, info := range nodes {
		core := strings.ReplaceAll(info.core, "\u00AD", "")
		if core == "" {
			continue
		}
		if sb.Len() > 0 {
			if endsWithDash(prevCore) && info.leadingWS == "" && prevTrailingWS == "" && startsWithLatinLetter(core) {
				trimmed := trimTrailingDash(sb.String())
				sb.Reset()
				sb.WriteString(trimmed)
			} else if shouldInsertSpace(prevCore, core, prevTrailingWS, info.leadingWS) {
				sb.WriteString(" ")
			}
		}
		sb.WriteString(core)
		prevCore = core
		prevTrailingWS = info.trailingWS
	}
	return sb.String()
}

// Extract extracts translatable text nodes fromxhtml/html files inside the epub.
func (p *EpubParser) Extract(inputPath string) ([]TextBlock, error) {
	p.originalZipPath = inputPath
	p.parsedFiles = make(map[string]*html.Node)
	p.blockInfos = make(map[string]*blockInfo)
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

			blockIndex := 0
			var traverse func(*html.Node)
			traverse = func(n *html.Node) {
				if isBlockElement(n) {
					var textNodes []*html.Node
					collectTextNodesForBlock(n, &textNodes)
					if len(textNodes) > 0 {
						info := &blockInfo{element: n}
						for _, tNode := range textNodes {
							leading, trailing, core := splitWhitespaceParts(tNode.Data)
							info.nodes = append(info.nodes, nodeTextInfo{
								node:       tNode,
								original:   tNode.Data,
								leadingWS:  leading,
								trailingWS: trailing,
								core:       core,
							})
						}
						blockText := buildBlockText(info.nodes)
						if strings.TrimSpace(blockText) != "" {
							id := fmt.Sprintf("%s_block_%d", name, blockIndex)
							blockIndex++
							p.blockInfos[id] = info
							blocks = append(blocks, TextBlock{
								ID:           id,
								OriginalText: blockText,
							})
						}
					}
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if isBlockElement(c) {
							traverse(c)
						} else {
							traverse(c)
						}
					}
					return
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

func findSplitIndex(textRunes []rune, target int) int {
	if target <= 0 {
		return 0
	}
	if target >= len(textRunes) {
		return len(textRunes)
	}
	best := target
	for i := target; i < len(textRunes); i++ {
		if unicode.IsSpace(textRunes[i]) {
			best = i + 1
			break
		}
	}
	for i := target; i >= 0; i-- {
		if unicode.IsSpace(textRunes[i]) {
			if target-i < best-target {
				best = i + 1
			}
			break
		}
	}
	return best
}

func splitTranslationByNodes(translated string, nodes []nodeTextInfo) []string {
	if translated == "" || len(nodes) == 0 {
		return nil
	}
	weights := make([]int, 0, len(nodes))
	totalWeight := 0
	for _, n := range nodes {
		w := len([]rune(n.core))
		if w <= 0 {
			w = 1
		}
		weights = append(weights, w)
		totalWeight += w
	}
	textRunes := []rune(translated)
	totalRunes := len(textRunes)
	if totalRunes == 0 {
		return make([]string, len(nodes))
	}
	segments := make([]string, len(nodes))
	used := 0
	accWeight := 0
	for i := 0; i < len(nodes); i++ {
		if i == len(nodes)-1 {
			segments[i] = string(textRunes[used:])
			break
		}
		accWeight += weights[i]
		target := int(float64(totalRunes) * float64(accWeight) / float64(totalWeight))
		if target < used {
			target = used
		}
		splitAt := findSplitIndex(textRunes, target)
		if splitAt < used {
			splitAt = target
		}
		remainingNodes := len(nodes) - i - 1
		if splitAt < used && totalRunes-used > remainingNodes {
			splitAt = used + 1
		}
		maxSplit := totalRunes - remainingNodes
		if splitAt > maxSplit {
			splitAt = maxSplit
		}
		segments[i] = string(textRunes[used:splitAt])
		used = splitAt
	}
	return segments
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

	for id, info := range p.blockInfos {
		translated, ok := transMap[id]
		if !ok || strings.TrimSpace(translated) == "" {
			continue
		}
		if strings.TrimSpace(translated) == "<!--merged-->" {
			if !bilingual {
				for _, n := range info.nodes {
					n.node.Data = ""
				}
			}
			continue
		}
		if bilingual {
			brNode := &html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"}
			translatedNode := &html.Node{Type: html.TextNode, Data: strings.TrimSpace(translated)}
			if info.element != nil {
				info.element.AppendChild(brNode)
				info.element.AppendChild(translatedNode)
			}
			continue
		}
		segments := splitTranslationByNodes(translated, info.nodes)
		for i, n := range info.nodes {
			segment := ""
			if i < len(segments) {
				segment = segments[i]
			}
			n.node.Data = preserveEdgeWhitespace(n.original, segment)
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
