package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ModeHTTP  = "http"
	ModeLocal = "local"

	DefaultMaxCatalogEntries = 1000
	MaxCatalogEntriesLimit   = 10000
)

type ResourceConfig struct {
	Enabled              bool `json:"enabled,omitempty"`
	SubscriptionsEnabled bool `json:"subscriptions_enabled,omitempty"`
	MaxCatalogEntries    int  `json:"max_catalog_entries,omitempty"`
}

// Config 控制 Wiki 工具使用远程 HTTP 后端还是本地 Markdown 后端。
type Config struct {
	Mode      string         `json:"mode"`
	Resources ResourceConfig `json:"resources,omitempty"`
	Local     LocalConfig    `json:"local"`
}

// LocalConfig 描述本地 llm-wiki 文章目录与索引刷新策略。
type LocalConfig struct {
	Root                   string                 `json:"root"`
	ContentDirs            []string               `json:"content_dirs"`
	WriteDir               string                 `json:"write_dir"`
	DefaultCategory        string                 `json:"default_category"`
	RefreshIntervalSeconds int                    `json:"refresh_interval_seconds"`
	MaxFileSizeBytes       int64                  `json:"max_file_size_bytes"`
	Users                  map[string]LocalConfig `json:"users,omitempty"`
	RequireUserMapping     bool                   `json:"require_user_mapping,omitempty"`
}

// LoadConfig 加载 Wiki 配置。配置文件不存在时保持历史行为，使用 HTTP 后端。
// local.root 的相对路径以配置文件所在目录为基准解析。
func LoadConfig(path string) (Config, error) {
	cfg := Config{Mode: ModeHTTP, Resources: ResourceConfig{MaxCatalogEntries: DefaultMaxCatalogEntries}}
	path = strings.TrimSpace(path)
	if path == "" {
		return cfg, nil
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open wiki config %q: %w", path, err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode wiki config %q: %w", path, err)
	}

	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = ModeHTTP
	}
	switch cfg.Mode {
	case ModeHTTP:
		if err := normalizeResourceConfig(&cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	case ModeLocal:
		if err := normalizeLocalConfig(&cfg.Local, filepath.Dir(path)); err != nil {
			return Config{}, err
		}
		if err := normalizeResourceConfig(&cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("unsupported wiki mode %q (want %q or %q)", cfg.Mode, ModeHTTP, ModeLocal)
	}
}

func normalizeResourceConfig(cfg *Config) error {
	if cfg.Resources.MaxCatalogEntries == 0 {
		cfg.Resources.MaxCatalogEntries = DefaultMaxCatalogEntries
	}
	if cfg.Resources.MaxCatalogEntries < 1 || cfg.Resources.MaxCatalogEntries > MaxCatalogEntriesLimit {
		return fmt.Errorf("wiki resources.max_catalog_entries must be between 1 and %d", MaxCatalogEntriesLimit)
	}
	if cfg.Resources.SubscriptionsEnabled {
		return errors.New("wiki resource subscriptions are not supported yet")
	}
	if cfg.Resources.Enabled && cfg.Mode != ModeLocal {
		return errors.New("wiki resources.enabled requires local mode")
	}
	return nil
}

func normalizeLocalConfig(cfg *LocalConfig, configDir string) error {
	users := cfg.Users
	requireUserMapping := cfg.RequireUserMapping
	cfg.Users = nil
	cfg.RequireUserMapping = false
	if err := normalizeLocalDirectory(cfg, configDir, "wiki local"); err != nil {
		return err
	}
	if requireUserMapping && len(users) == 0 {
		return errors.New("wiki local.require_user_mapping requires at least one local.users entry")
	}
	normalizedUsers := make(map[string]LocalConfig, len(users))
	for rawUserID, userCfg := range users {
		userID := strings.TrimSpace(rawUserID)
		if userID == "" {
			return errors.New("wiki local.users contains an empty userId")
		}
		if userID != rawUserID {
			return fmt.Errorf("wiki local.users key %q must not contain surrounding whitespace", rawUserID)
		}
		if len(userCfg.Users) > 0 || userCfg.RequireUserMapping {
			return fmt.Errorf("wiki local.users[%q] must not contain nested routing settings", userID)
		}
		if err := normalizeLocalDirectory(&userCfg, configDir, fmt.Sprintf("wiki local.users[%q]", userID)); err != nil {
			return err
		}
		normalizedUsers[userID] = userCfg
	}
	cfg.Users = normalizedUsers
	cfg.RequireUserMapping = requireUserMapping
	return nil
}

func normalizeLocalDirectory(cfg *LocalConfig, configDir, fieldPrefix string) error {
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return fmt.Errorf("%s.root must not be empty in local mode", fieldPrefix)
	}
	if strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(configDir, root)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve wiki local.root %q: %w", root, err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return fmt.Errorf("stat wiki local.root %q: %w", absRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("wiki local.root %q is not a directory", absRoot)
	}
	cfg.Root = filepath.Clean(absRoot)

	if len(cfg.ContentDirs) == 0 {
		cfg.ContentDirs = []string{"wiki"}
	}
	for i, dir := range cfg.ContentDirs {
		clean, err := cleanRelativePath(dir, fieldPrefix+".content_dirs entry")
		if err != nil {
			return err
		}
		cfg.ContentDirs[i] = clean
	}
	if strings.TrimSpace(cfg.WriteDir) == "" {
		cfg.WriteDir = filepath.Join(cfg.ContentDirs[0], "topics")
	}
	writeDir, err := cleanRelativePath(cfg.WriteDir, fieldPrefix+".write_dir")
	if err != nil {
		return err
	}
	if !withinAnyContentDir(writeDir, cfg.ContentDirs) {
		return fmt.Errorf("%s.write_dir %q must be inside a configured content_dir", fieldPrefix, cfg.WriteDir)
	}
	cfg.WriteDir = writeDir
	cfg.DefaultCategory = strings.TrimSpace(cfg.DefaultCategory)
	if cfg.DefaultCategory == "" {
		cfg.DefaultCategory = "topics"
	}
	if cfg.RefreshIntervalSeconds <= 0 {
		cfg.RefreshIntervalSeconds = 30
	}
	if cfg.MaxFileSizeBytes <= 0 {
		cfg.MaxFileSizeBytes = 2 << 20
	}
	return nil
}

func cleanRelativePath(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("%s %q must be relative to local.root", field, value)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q escapes local.root", field, value)
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%s %q contains a hidden path component", field, value)
		}
	}
	return clean, nil
}

func withinAnyContentDir(path string, contentDirs []string) bool {
	for _, contentDir := range contentDirs {
		rel, err := filepath.Rel(contentDir, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c LocalConfig) RefreshInterval() time.Duration {
	return time.Duration(c.RefreshIntervalSeconds) * time.Second
}
