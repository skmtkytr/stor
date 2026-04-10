package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// AcquirePIDFile writes the current PID to the file.
// If a previous daemon is still running, it is killed first.
func AcquirePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Check for existing PID file
	if data, err := os.ReadFile(path); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			// Check if process is alive
			proc, err := os.FindProcess(pid)
			if err == nil {
				// Signal 0 checks existence without killing
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					// Process is alive — kill it
					fmt.Printf("Stopping previous daemon (PID %d)...\n", pid)
					_ = proc.Signal(syscall.SIGTERM)

					// Wait briefly for it to die
					for range 20 { // 2 seconds max
						if err := proc.Signal(syscall.Signal(0)); err != nil {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}

					// Force kill if still alive
					if err := proc.Signal(syscall.Signal(0)); err == nil {
						_ = proc.Signal(syscall.SIGKILL)
					}
				}
			}
		}
	}

	// Write our PID
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ReleasePIDFile removes the PID file.
func ReleasePIDFile(path string) {
	_ = os.Remove(path)
}
