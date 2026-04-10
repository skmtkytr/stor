package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds daemon configuration.
type Config struct {
	Port        int    `json:"port"`
	DownloadDir string `json:"download_dir"`
	APIKey      string `json:"api_key"`
	StatePath   string `json:"state_path"`
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "stor")
	return Config{
		Port:        9090,
		DownloadDir: filepath.Join(home, "Downloads"),
		StatePath:   filepath.Join(configDir, "state.json"),
	}
}

// LoadConfig reads config from a file. If the file doesn't exist,
// creates it with defaults and a generated API key.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.APIKey = generateAPIKey()
			if err := saveConfig(path, cfg); err != nil {
				return cfg, fmt.Errorf("daemon: save default config: %w", err)
			}
			return cfg, nil
		}
		return cfg, fmt.Errorf("daemon: read config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("daemon: parse config: %w", err)
	}

	// Generate API key if missing
	if cfg.APIKey == "" {
		cfg.APIKey = generateAPIKey()
		if err := saveConfig(path, cfg); err != nil {
			return cfg, fmt.Errorf("daemon: save config with key: %w", err)
		}
	}

	return cfg, nil
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func generateAPIKey() string {
	var buf [32]byte
	_, _ = rand.Read(buf[:])
	return "sk-" + hex.EncodeToString(buf[:])
}
