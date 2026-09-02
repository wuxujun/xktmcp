package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaultsResourcesDisabled(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resources.Enabled || cfg.Resources.SubscriptionsEnabled {
		t.Fatalf("resources defaults = %+v, want disabled", cfg.Resources)
	}
	if cfg.Resources.MaxCatalogEntries != 1000 {
		t.Fatalf("max_catalog_entries = %d, want 1000", cfg.Resources.MaxCatalogEntries)
	}
}

func TestLoadConfigNormalizesResources(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wiki.json")
	raw := `{"mode":"local","resources":{"enabled":true},"local":{"root":"."}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Resources.Enabled || cfg.Resources.MaxCatalogEntries != 1000 {
		t.Fatalf("resources = %+v", cfg.Resources)
	}
}

func TestLoadConfigRejectsInvalidResources(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"http enabled", `{"mode":"http","resources":{"enabled":true}}`, "wiki resources.enabled requires local mode"},
		{"subscriptions without resources", `{"mode":"local","resources":{"subscriptions_enabled":true},"local":{"root":"ROOT"}}`, "wiki resource subscriptions are not supported yet"},
		{"subscriptions not implemented", `{"mode":"local","resources":{"enabled":true,"subscriptions_enabled":true},"local":{"root":"ROOT"}}`, "wiki resource subscriptions are not supported yet"},
		{"negative limit", `{"mode":"local","resources":{"enabled":true,"max_catalog_entries":-1},"local":{"root":"ROOT"}}`, "wiki resources.max_catalog_entries must be between 1 and 10000"},
		{"large limit", `{"mode":"local","resources":{"enabled":true,"max_catalog_entries":10001},"local":{"root":"ROOT"}}`, "wiki resources.max_catalog_entries must be between 1 and 10000"},
		{"multi tenant subscriptions", `{"mode":"local","resources":{"enabled":true,"subscriptions_enabled":true},"local":{"root":"ROOT","users":{"u1":{"root":"ROOT"}}}}`, "wiki resource subscriptions are not supported yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "wiki"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "wiki.json")
			raw := strings.ReplaceAll(tt.raw, "ROOT", ".")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("LoadConfig error = %v, want %q", err, tt.want)
			}
		})
	}
}

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

func TestLoadConfigNormalizesPerUserDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"default/wiki", "alice/wiki", "bob/wiki"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "wiki.json")
	config := `{
  "mode":"local",
  "local":{
    "root":"default",
    "require_user_mapping":true,
    "users":{
      "alice":{"root":"alice"},
      "bob":{"root":"bob"}
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if got := cfg.Local.Users["alice"].Root; got != filepath.Join(dir, "alice") {
		t.Fatalf("alice root = %q", got)
	}
	if !cfg.Local.RequireUserMapping {
		t.Fatal("require_user_mapping = false, want true")
	}
}
