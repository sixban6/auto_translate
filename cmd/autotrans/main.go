package main

import (
	"flag"
	"log"
	"path/filepath"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "c", "config.json", "Path to configuration file")
	flag.Parse()

	log.SetFlags(log.Ltime)

	log.Printf("Loading configuration from %s...", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.SystemInfoMsg != "" {
		log.Println(cfg.SystemInfoMsg)
	}
	if cfg.SystemWarning != "" {
		log.Println(cfg.SystemWarning)
	}

	ext := filepath.Ext(cfg.InputFile)
	p, err := parser.GetParser(ext)
	if err != nil {
		log.Fatalf("Failed to initialize parser for %s: %v", ext, err)
	}

	log.Printf("Extracting text from %s...", cfg.InputFile)
	blocks, err := p.Extract(cfg.InputFile)
	if err != nil {
		log.Fatalf("Failed to extract blocks: %v", err)
	}
	log.Printf("Extracted %d translatable text blocks.", len(blocks))

	tr := translator.New(cfg)
	proc := processor.New(cfg, tr)

	log.Printf("Starting translation with concurrency %d...", cfg.Concurrency)
	startTime := time.Now()

	translatedBlocks, err := proc.Process(blocks, func(current, total int, msg string) {
		if msg != "" {
			log.Println(msg)
		} else if current%10 == 0 || current == total {
			log.Printf("Progress: %d/%d (%.1f%%)", current, total, float64(current)/float64(total)*100)
		}
	})
	if err != nil {
		log.Fatalf("Translation failed: %v", err)
	}

	log.Printf("Translation finished in %v. Assembling output...", time.Since(startTime))
	err = p.Assemble(translatedBlocks, cfg.OutputFile, cfg.Bilingual)
	if err != nil {
		log.Fatalf("Failed to assemble output file: %v", err)
	}

	log.Printf("Success! Output written to %s", cfg.OutputFile)
}
