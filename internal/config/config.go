// Package config loads user settings for herdr-outbox from a YAML file in the
// message directory.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EditorMode selects how the TUI opens a message body for editing.
type EditorMode string

const (
	// EditorBuiltin edits inside the TUI with an in-app text area.
	EditorBuiltin EditorMode = "builtin"
	// EditorExternal hands the terminal to $EDITOR / $VISUAL.
	EditorExternal EditorMode = "external"
)

const FileName = "config.yaml"

type Config struct {
	// Editor chooses the editing surface. Empty means EditorBuiltin.
	Editor EditorMode `yaml:"editor"`
}

// Default returns the settings used when no config file exists.
func Default() Config {
	return Config{Editor: EditorBuiltin}
}

func Path(dir string) string { return filepath.Join(dir, FileName) }

// EditorMode resolves the configured editor, treating an unset or unrecognised
// value as the default.
func (c Config) ResolveEditor() EditorMode {
	switch c.Editor {
	case EditorExternal:
		return EditorExternal
	case EditorBuiltin, "":
		return EditorBuiltin
	default:
		return EditorBuiltin
	}
}

// Load reads <dir>/config.yaml. A missing file is not an error.
func Load(dir string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", Path(dir), err)
	}
	cfg.Editor = EditorMode(strings.TrimSpace(string(cfg.Editor)))
	switch cfg.ResolveEditor() {
	case EditorBuiltin:
		if cfg.Editor != "" && cfg.Editor != EditorBuiltin {
			return cfg, fmt.Errorf("unknown editor %q in %s; expected builtin or external", string(cfg.Editor), Path(dir))
		}
	}
	return cfg, nil
}

// Write stores the config with a commented header so the file explains itself.
func Write(dir string, cfg Config) error {
	body := "# herdr-outbox 配置\n# editor: builtin 在 TUI 内编辑；external 使用 $EDITOR\n"
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	body += string(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return os.WriteFile(Path(dir), []byte(body), 0o644)
}
