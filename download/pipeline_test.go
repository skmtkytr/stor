package download

import (
	"testing"
)

// TestPipelineTargetClampLow verifies the BDP-derived pipeline depth is
// clamped to the minimum when the observed downRate is below the
// breakeven point (0 bytes/sec on a fresh peer must still allow MIN
// outstanding requests, not zero — otherwise a peer that just connected
// could never start filling its pipeline).
func TestPipelineTargetClampLow(t *testing.T) {
	got := pipelineTarget(0, 3, 4, 256)
	if got != 4 {
		t.Errorf("pipelineTarget(0 B/s) = %d, want %d (min)", got, 4)
	}
}

// TestPipelineTargetClampHigh verifies the BDP-derived pipeline depth is
// clamped to the maximum so a fast peer doesn't blow up local memory or
// trip the remote's request-queue limit.
func TestPipelineTargetClampHigh(t *testing.T) {
	// 100 MB/s * 3s / 16 KiB = ~19200 requests — way over any sane cap.
	got := pipelineTarget(100<<20, 3, 4, 256)
	if got != 256 {
		t.Errorf("pipelineTarget(100 MB/s) = %d, want %d (max)", got, 256)
	}
}

// TestPipelineTargetFormula verifies the BDP formula at a mid-range rate.
// 1 MiB/s * 3 s = 3 MiB, divided by the 16 KiB block size = 192 requests.
// 192 sits within [4, 256] so neither clamp kicks in.
func TestPipelineTargetFormula(t *testing.T) {
	got := pipelineTarget(1<<20, 3, 4, 256)
	want := (1 << 20) * 3 / BlockSize // = 192
	if got != want {
		t.Errorf("pipelineTarget(1 MiB/s, 3s) = %d, want %d", got, want)
	}
}

// TestPipelineTargetWindowEffect verifies that window seconds linearly
// scale the target — doubling the window doubles the outstanding count
// (within clamp bounds). This is the knob users actually tune.
func TestPipelineTargetWindowEffect(t *testing.T) {
	t1 := pipelineTarget(512<<10, 1, 4, 1024) // 512 KiB/s * 1s
	t3 := pipelineTarget(512<<10, 3, 4, 1024) // 512 KiB/s * 3s
	if t3 != 3*t1 {
		t.Errorf("window scaling broken: t1=%d t3=%d, expected t3=3*t1", t1, t3)
	}
}

// TestPipelineTargetMinEqualsMaxStaysFixed verifies the back-compat path:
// when min == max (or the user sets only the legacy MaxPipeline), the
// target collapses to that single value regardless of observed rate.
func TestPipelineTargetMinEqualsMaxStaysFixed(t *testing.T) {
	const fixed = 16
	for _, rate := range []int64{0, 1, 1 << 10, 1 << 20, 100 << 20} {
		if got := pipelineTarget(rate, 3, fixed, fixed); got != fixed {
			t.Errorf("pipelineTarget(%d, min==max==%d) = %d, want %d", rate, fixed, got, fixed)
		}
	}
}

// TestPipelineTargetReadsClientDownRate verifies the integration point:
// Client.PipelineTarget pulls the current EMA off the atomic downRate
// counter without holding a lock, so DownloadPiece can recompute every
// loop iteration cheaply.
func TestPipelineTargetReadsClientDownRate(t *testing.T) {
	c := &Client{
		maxPipeline:    16, // legacy fixed (used as both min and max)
		pipelineMin:    4,
		pipelineMax:    64,
		pipelineWindow: 3,
	}
	c.downRate.Store(256 << 10) // 256 KiB/s
	want := pipelineTarget(256<<10, 3, 4, 64)
	if got := c.PipelineTarget(); got != want {
		t.Errorf("Client.PipelineTarget() = %d, want %d", got, want)
	}
}

// TestPipelineTargetLegacyClient covers the post-handshake path before
// runWorkers overrides the pipeline fields: a freshly-constructed
// Client with only maxPipeline set must behave like the old fixed-depth
// scheduler. This protects newClient + the upload path from regressing
// when peers connect via NewClient (without DownloadConfig wiring).
func TestPipelineTargetLegacyClient(t *testing.T) {
	c := &Client{maxPipeline: DefaultMaxPipeline}
	c.downRate.Store(10 << 20) // 10 MiB/s — would otherwise blow past the cap
	if got := c.PipelineTarget(); got != DefaultMaxPipeline {
		t.Errorf("legacy client PipelineTarget = %d, want %d", got, DefaultMaxPipeline)
	}
}
