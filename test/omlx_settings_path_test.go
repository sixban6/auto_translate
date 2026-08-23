package test

import (
	"os"
	"path/filepath"
	"testing"

	"auto_translate/pkg/config"
)

func TestOMLXSettingsPath_DesktopBasePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	marker := filepath.Join(home, "Library", "Application Support", "oMLX", "base-path")
	if _, err := os.Stat(marker); err != nil {
		t.Skip("no desktop oMLX installation on this machine")
	}
	p := config.OMLXSettingsPath()
	if p == filepath.Join(home, ".omlx", "settings.json") && p != "" {
		t.Fatalf("should resolve the desktop base path, got legacy path %s", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("resolved settings path does not exist: %s", p)
	}
	if k := config.ReadOMLXAPIKey(); k == "" {
		t.Fatalf("API key not readable from %s", p)
	}
}
