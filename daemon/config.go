package daemon

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds daemon configuration.
type Config struct {
	Port        int    `toml:"port"`
	DownloadDir string `toml:"download_dir"`
	APIKey      string `toml:"api_key"`
	StatePath   string `toml:"state_path"`
	MaxActive   int    `toml:"max_active"`

	// Logging
	LogLevel string `toml:"log_level"` // debug, info, warn, error (default: info)

	// Performance tuning (0 = use defaults)
	MaxPeers    int `toml:"max_peers"`
	MaxPipeline int `toml:"max_pipeline"`
	DialTimeout int `toml:"dial_timeout"`
	NumWant     int `toml:"numwant"`
	DHTAlpha    int `toml:"dht_alpha"`

	path string // file path for saving back
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "stor")
	return Config{
		Port:        9090,
		DownloadDir: filepath.Join(home, "Downloads"),
		StatePath:   filepath.Join(configDir, "state.json"),
		MaxActive:   5,
		LogLevel:    "info",
	}
}

// LoadConfig reads config from a TOML file. If the file doesn't exist,
// creates it with defaults and a generated API key.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	cfg.path = path

	// Default state_path to same directory as config file
	cfg.StatePath = filepath.Join(filepath.Dir(path), "state.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.APIKey = generateAPIKey()
			if err := cfg.Save(); err != nil {
				return cfg, fmt.Errorf("daemon: save default config: %w", err)
			}
			return cfg, nil
		}
		return cfg, fmt.Errorf("daemon: read config: %w", err)
	}

	parseTOML(string(data), &cfg)

	if cfg.APIKey == "" {
		cfg.APIKey = generateAPIKey()
		if err := cfg.Save(); err != nil {
			return cfg, fmt.Errorf("daemon: save config with key: %w", err)
		}
	}
	if cfg.MaxActive < 1 {
		cfg.MaxActive = 5
	}
	// If state_path is empty or uses a non-existent home dir, default to config dir
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(filepath.Dir(path), "state.json")
	}

	return cfg, nil
}

// Save writes the config to disk as TOML.
func (c *Config) Save() error {
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}

	content := fmt.Sprintf(`# stor daemon configuration

port = %d
download_dir = %q
api_key = %q
state_path = %q
max_active = %d
log_level = %q
`, c.Port, c.DownloadDir, c.APIKey, c.StatePath, c.MaxActive, c.LogLevel)

	return os.WriteFile(c.path, []byte(content), 0o600)
}

// parseTOML is a minimal TOML parser for flat key = value pairs.
func parseTOML(data string, cfg *Config) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == '[' {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// Unquote string values
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val, _ = strconv.Unquote(val)
		}

		switch key {
		case "port":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Port = n
			}
		case "download_dir":
			cfg.DownloadDir = val
		case "api_key":
			cfg.APIKey = val
		case "state_path":
			cfg.StatePath = val
		case "max_active":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.MaxActive = n
			}
		case "log_level":
			cfg.LogLevel = val
		case "max_peers":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.MaxPeers = n
			}
		case "max_pipeline":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.MaxPipeline = n
			}
		case "dial_timeout":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.DialTimeout = n
			}
		case "numwant":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.NumWant = n
			}
		case "dht_alpha":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.DHTAlpha = n
			}
		}
	}
}

// ParseLogLevel converts a log level string to slog.Level.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func generateAPIKey() string {
	var buf [32]byte
	_, _ = rand.Read(buf[:])
	return "sk-" + hex.EncodeToString(buf[:])
}
