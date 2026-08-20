package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsToHTTPWhenMissing(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Mode != ModeHTTP {
		t.Fatalf("mode = %q, want %q", cfg.Mode, ModeHTTP)
	}
}

func TestLoadConfigLocalResolvesRelativeRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "knowledge")
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "wiki.json")
	config := `{"mode":"local","local":{"root":"knowledge"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatalf("mode = %q, want %q", cfg.Mode, ModeLocal)
	}
	if cfg.Local.Root != root {
		t.Fatalf("root = %q, want %q", cfg.Local.Root, root)
	}
	if len(cfg.Local.ContentDirs) != 1 || cfg.Local.ContentDirs[0] != "wiki" {
		t.Fatalf("content_dirs = %v, want [wiki]", cfg.Local.ContentDirs)
	}
	if cfg.Local.WriteDir != filepath.Join("wiki", "topics") {
		t.Fatalf("write_dir = %q, want wiki/topics", cfg.Local.WriteDir)
	}
	if cfg.Local.DefaultCategory != "topics" {
		t.Fatalf("default_category = %q, want topics", cfg.Local.DefaultCategory)
	}
}

func TestLoadConfigRejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wiki.json")
	if err := os.WriteFile(configPath, []byte(`{"mode":"database"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig succeeded for unsupported mode")
	}
}

func TestLoadConfigRejectsWriteDirOutsideContentDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "wiki.json")
	config := `{"mode":"local","local":{"root":".","content_dirs":["wiki"],"write_dir":"raw"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig accepted write_dir outside content_dirs")
	}
}
