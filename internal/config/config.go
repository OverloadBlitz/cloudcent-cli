package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds CLI authentication credentials.
type Config struct {
	CliID  string  `yaml:"cli_id"`
	APIKey *string `yaml:"api_key,omitempty"`
}

// Dir returns ~/.cloudcent, creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	dir := filepath.Join(home, ".cloudcent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	return dir, nil
}

// Path returns the full path to config.yaml.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads ~/.cloudcent/config.yaml. Returns nil, nil if missing.
// If the CLOUDCENT_API_KEY environment variable is set, it takes precedence
// over the config file (useful for CI environments).
func Load() (*Config, error) {
	// Environment variable takes precedence — no file needed in CI.
	if apiKey := os.Getenv("CLOUDCENT_API_KEY"); apiKey != "" {
		cliID := os.Getenv("CLOUDCENT_CLI_ID") // optional
		return &Config{CliID: cliID, APIKey: &apiKey}, nil
	}

	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}
	return &cfg, nil
}

// Save writes config to ~/.cloudcent/config.yaml with 0600 permissions.
func Save(cfg *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// MetadataGzPath returns ~/.cloudcent/metadata.json.gz
func MetadataGzPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metadata.json.gz"), nil
}

// DBPath returns ~/.cloudcent/cloudcent.db
func DBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cloudcent.db"), nil
}
