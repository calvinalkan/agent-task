package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

// Config holds all configuration options.
type Config struct {
	TicketDir string `json:"ticket_dir"` //nolint:tagliatelle // snake_case for config file
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		TicketDir: ".tickets",
	}
}

// ConfigFileName is the default config file name.
const ConfigFileName = ".tk.json"

// LoadConfig loads configuration with the following precedence (highest wins):
// 1. Defaults
// 2. Config file at default location (if exists)
// 3. Explicit config file via configPath (if non-empty)
// 4. CLI overrides.
func LoadConfig(workDir, configPath string, cliOverrides Config, hasTicketDirOverride bool) (Config, error) {
	cfg := DefaultConfig()

	// Determine which config file to load
	var cfgFile string
	if configPath != "" {
		// Explicit config file - must exist
		cfgFile = configPath
		if !filepath.IsAbs(cfgFile) {
			cfgFile = filepath.Join(workDir, cfgFile)
		}

		_, statErr := os.Stat(cfgFile)
		if statErr != nil {
			return Config{}, fmt.Errorf("%w: %s", errConfigFileNotFound, configPath)
		}
	} else {
		// Default config file - optional
		cfgFile = filepath.Join(workDir, ConfigFileName)
	}

	// Load config file if it exists
	data, readErr := os.ReadFile(cfgFile) //nolint:gosec // cfgFile is intentionally user-controlled
	if readErr == nil {
		fileCfg, explicitEmpty, parseErr := parseConfig(data)
		if parseErr != nil {
			return Config{}, fmt.Errorf("%w %s: %w", errConfigInvalid, cfgFile, parseErr)
		}

		// If ticket_dir was explicitly set to empty string in file, that's an error
		if explicitEmpty["ticket_dir"] {
			return Config{}, fmt.Errorf("%w %s: %w", errConfigInvalid, cfgFile, errTicketDirEmpty)
		}

		cfg = mergeConfig(cfg, fileCfg)
	} else if configPath != "" {
		// Explicit config was specified but couldn't be read
		return Config{}, fmt.Errorf("%w: %s", errConfigFileRead, configPath)
	}

	// Apply CLI overrides
	if hasTicketDirOverride {
		cfg.TicketDir = cliOverrides.TicketDir
	}

	// Validate
	validateErr := validateConfig(cfg)
	if validateErr != nil {
		return Config{}, validateErr
	}

	return cfg, nil
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
