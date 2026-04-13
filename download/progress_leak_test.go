package download

import (
	"runtime"
	"testing"
	"time"
)

// TestNewProgressGoroutineLeak verifies that creating and discarding a Progress
// without calling Close() does NOT leak goroutines.
// The fix: callers must call Close(), or Progress must auto-close.
// Here we test that Close() properly stops goroutines.
func TestNewProgressGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	// Create and close many Progress instances
	for range 10 {
		p := NewProgress(100, 100*256*1024)
		p.Close()
	}

	// Allow goroutines to wind down
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()

	// Should not have leaked goroutines (allow +2 for runtime jitter)
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d, after=%d (delta=%d)", before, after, after-before)
	}
}

// TestProgressSnapIncludesUpSpeed verifies that Snap() populates the UpSpeed field.
func TestProgressSnapIncludesUpSpeed(t *testing.T) {
	p := NewProgress(10, 1000)
	defer p.Close()

	p.AddUploadBytes(5000)
	// Wait for at least one EMA tick
	time.Sleep(1200 * time.Millisecond)

	snap := p.Snap()
	if snap.UpSpeed == 0 {
		t.Error("Snap().UpSpeed should be non-zero after AddUploadBytes + tick")
	}
}
