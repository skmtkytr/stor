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
	TmpDir      string `toml:"tmp_dir"` // temp dir for in-progress downloads; empty = use download_dir
	APIKey      string `toml:"api_key"`
	StatePath   string // always derived from config file dir
	MaxActive   int    `toml:"max_active"`

	// Logging
	LogLevel string `toml:"log_level"` // debug, info, warn, error (default: info)

	// Performance tuning (0 = use defaults)
	MaxPeers    int  `toml:"max_peers"`
	MaxPipeline int  `toml:"max_pipeline"`
	DialTimeout int  `toml:"dial_timeout"`
	NumWant     int  `toml:"numwant"`
	DHTAlpha    int  `toml:"dht_alpha"`
	EnableUTP   bool `toml:"enable_utp"`

	path string // file path for saving back
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Port:        9090,
		DownloadDir: filepath.Join(home, "Downloads"),
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
	// Always place state.json next to config file
	cfg.StatePath = filepath.Join(filepath.Dir(path), "state.json")

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

	var b strings.Builder
	fmt.Fprintf(&b, "# stor daemon configuration\n\n")
	fmt.Fprintf(&b, "port = %d\n", c.Port)
	fmt.Fprintf(&b, "download_dir = %q\n", c.DownloadDir)
	if c.TmpDir != "" {
		fmt.Fprintf(&b, "tmp_dir = %q\n", c.TmpDir)
	}
	fmt.Fprintf(&b, "api_key = %q\n", c.APIKey)
	fmt.Fprintf(&b, "max_active = %d\n", c.MaxActive)
	fmt.Fprintf(&b, "log_level = %q\n", c.LogLevel)
	fmt.Fprintf(&b, "\n# Performance tuning (0 = use defaults)\n")
	if c.MaxPeers > 0 {
		fmt.Fprintf(&b, "max_peers = %d\n", c.MaxPeers)
	}
	if c.MaxPipeline > 0 {
		fmt.Fprintf(&b, "max_pipeline = %d\n", c.MaxPipeline)
	}
	if c.DialTimeout > 0 {
		fmt.Fprintf(&b, "dial_timeout = %d\n", c.DialTimeout)
	}
	if c.NumWant > 0 {
		fmt.Fprintf(&b, "numwant = %d\n", c.NumWant)
	}
	if c.DHTAlpha > 0 {
		fmt.Fprintf(&b, "dht_alpha = %d\n", c.DHTAlpha)
	}
	if c.EnableUTP {
		fmt.Fprintf(&b, "enable_utp = true\n")
	}

	return os.WriteFile(c.path, []byte(b.String()), 0o600)
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
		case "tmp_dir":
			cfg.TmpDir = val
		case "api_key":
			cfg.APIKey = val
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
		case "enable_utp":
			cfg.EnableUTP = val == "true"
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
