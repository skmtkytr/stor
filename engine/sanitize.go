package engine

import (
	"fmt"
	"path/filepath"
	"strings"
)

// sanitizeTorrentName validates a torrent name from metadata to prevent
// path traversal attacks. Returns an error if the name is unsafe.
func sanitizeTorrentName(name string) error {
	if name == "" {
		return fmt.Errorf("engine: empty torrent name")
	}

	// Reject null bytes
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("engine: torrent name contains null byte")
	}

	// Reject absolute paths (unix and windows)
	if filepath.IsAbs(name) {
		return fmt.Errorf("engine: torrent name is absolute path: %q", name)
	}
	// Windows drive letter (e.g. C:\)
	if len(name) >= 2 && name[1] == ':' {
		return fmt.Errorf("engine: torrent name contains drive letter: %q", name)
	}

	// Normalize separators: treat backslash as separator too (Windows torrents)
	normalized := strings.ReplaceAll(name, "\\", "/")

	// Reject any ".." or "." component
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("engine: torrent name contains path traversal: %q", name)
		}
	}

	return nil
}
