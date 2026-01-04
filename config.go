package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// Config holds all configuration options.
type Config struct {
	TicketDir string `json:"ticket_dir"` //nolint:tagliatelle // snake_case for config file
	Editor    string `json:"editor,omitempty"`
}

// ConfigSources tracks which config files were loaded.
type ConfigSources struct {
	Global  string // Path to global config if loaded, empty otherwise
	Project string // Path to project config if loaded, empty otherwise
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		TicketDir: ".tickets",
	}
}

// ConfigFileName is the default config file name.
const ConfigFileName = ".tk.json"

// getGlobalConfigPath returns the path to the global config file.
// Uses $XDG_CONFIG_HOME/tk/config.json if set, otherwise ~/.config/tk/config.json.
// Returns empty string if home directory cannot be determined.
func getGlobalConfigPath(env []string) string {
	// Check for XDG_CONFIG_HOME in the provided env slice first
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, "XDG_CONFIG_HOME="); ok {
			return filepath.Join(after, "tk", "config.json")
		}
	}

	// Fall back to os.Getenv
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "tk", "config.json")
	}

	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".config", "tk", "config.json")
	}

	return ""
}

// LoadConfig loads configuration with the following precedence (highest wins):
// 1. Defaults
// 2. Global user config (~/.config/tk/config.json or $XDG_CONFIG_HOME/tk/config.json)
// 3. Project config file at default location (.tk.json, if exists)
// 4. Explicit config file via configPath (if non-empty)
// 5. CLI overrides.
func LoadConfig(
	workDir, configPath string, cliOverrides Config, hasTicketDirOverride bool, env []string,
) (Config, ConfigSources, error) {
	cfg := DefaultConfig()

	var sources ConfigSources

	// Load global config if it exists
	globalCfg, globalPath, err := loadGlobalConfig(env)
	if err != nil {
		return Config{}, ConfigSources{}, err
	}

	sources.Global = globalPath
	cfg = mergeConfig(cfg, globalCfg)

	// Load project/explicit config file
	projectCfg, projectPath, err := loadProjectConfig(workDir, configPath)
	if err != nil {
		return Config{}, ConfigSources{}, err
	}

	sources.Project = projectPath
	cfg = mergeConfig(cfg, projectCfg)

	// Apply CLI overrides
	if hasTicketDirOverride {
		cfg.TicketDir = cliOverrides.TicketDir
	}

	// Validate
	validateErr := validateConfig(cfg)
	if validateErr != nil {
		return Config{}, ConfigSources{}, validateErr
	}

	return cfg, sources, nil
}

// loadGlobalConfig loads the global user config file if it exists.
// Returns the config, the path if loaded, and any error.
func loadGlobalConfig(env []string) (Config, string, error) {
	globalCfgPath := getGlobalConfigPath(env)
	if globalCfgPath == "" {
		return Config{}, "", nil
	}

	globalCfg, explicitEmpty, loaded, err := loadConfigFile(globalCfgPath, false)
	if err != nil {
		return Config{}, "", err
	}

	if !loaded {
		return Config{}, "", nil
	}

	if explicitEmpty["ticket_dir"] {
		return Config{}, "", fmt.Errorf("%w %s: %w", errConfigInvalid, globalCfgPath, errTicketDirEmpty)
	}

	return globalCfg, globalCfgPath, nil
}

// loadProjectConfig loads the project config file (.tk.json) or an explicit config file.
// Returns the config, the path if loaded, and any error.
func loadProjectConfig(workDir, configPath string) (Config, string, error) {
	var cfgFile string

	var mustExist bool

	if configPath != "" {
		// Explicit config file - must exist
		cfgFile = configPath
		if !filepath.IsAbs(cfgFile) {
			cfgFile = filepath.Join(workDir, cfgFile)
		}

		mustExist = true

		// Check existence first to provide a clear "not found" error
		_, statErr := os.Stat(cfgFile)
		if statErr != nil {
			return Config{}, "", fmt.Errorf("%w: %s", errConfigFileNotFound, configPath)
		}
	} else {
		// Default project config file - optional
		cfgFile = filepath.Join(workDir, ConfigFileName)
		mustExist = false
	}

	fileCfg, explicitEmpty, loaded, err := loadConfigFile(cfgFile, mustExist)
	if err != nil {
		return Config{}, "", err
	}

	if !loaded {
		return Config{}, "", nil
	}

	if explicitEmpty["ticket_dir"] {
		return Config{}, "", fmt.Errorf("%w %s: %w", errConfigInvalid, cfgFile, errTicketDirEmpty)
	}

	return fileCfg, cfgFile, nil
}

// loadConfigFile loads a config file. If mustExist is false, missing files return zero config.
// Returns the config, a map of explicitly empty fields, whether file was loaded, and any error.
func loadConfigFile(path string, mustExist bool) (Config, map[string]bool, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is intentionally user-controlled
	if err != nil {
		if os.IsNotExist(err) && !mustExist {
			return Config{}, nil, false, nil
		}

		if mustExist {
			return Config{}, nil, false, fmt.Errorf("%w: %s", errConfigFileRead, path)
		}

		return Config{}, nil, false, nil
	}

	cfg, explicitEmpty, parseErr := parseConfig(data)
	if parseErr != nil {
		return Config{}, nil, false, fmt.Errorf("%w %s: %w", errConfigInvalid, path, parseErr)
	}

	return cfg, explicitEmpty, true, nil
}

func parseConfig(data []byte) (Config, map[string]bool, error) {
	// Standardize JSONC to JSON
	standardized, err := hujson.Standardize(data)
	if err != nil {
		return Config{}, nil, fmt.Errorf("invalid JSONC: %w", err)
	}

	var cfg Config

	unmarshalErr := json.Unmarshal(standardized, &cfg)
	if unmarshalErr != nil {
		return Config{}, nil, fmt.Errorf("invalid JSON: %w", unmarshalErr)
	}

	// Check which fields were explicitly set to empty
	var raw map[string]any

	_ = json.Unmarshal(standardized, &raw)

	explicitEmpty := make(map[string]bool)

	if val, exists := raw["ticket_dir"]; exists {
		if str, ok := val.(string); ok && str == "" {
			explicitEmpty["ticket_dir"] = true
		}
	}

	return cfg, explicitEmpty, nil
}

func mergeConfig(base, overlay Config) Config {
	if overlay.TicketDir != "" {
		base.TicketDir = overlay.TicketDir
	}

	if overlay.Editor != "" {
		base.Editor = overlay.Editor
	}

	return base
}

func validateConfig(cfg Config) error {
	if cfg.TicketDir == "" {
		return errTicketDirEmpty
	}

	return nil
}

// FormatConfig returns the config as formatted JSON.
func FormatConfig(cfg Config) (string, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format config: %w", err)
	}

	return string(data), nil
}
