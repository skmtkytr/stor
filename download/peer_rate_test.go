package download

import (
	"testing"
	"time"
)

// TestSnapshotUsesEMAWhenPresent verifies that a Client with downEMA/upEMA
// populated reports the EMA rate in PeerSnap, not the cumulative
// rechoke-window average. This is what keeps the per-peer Down/Up values
// in the UI in sync with the torrent-wide EMA shown in the top row.
func TestSnapshotUsesEMAWhenPresent(t *testing.T) {
	c := &Client{
		Addr:       "1.2.3.4:6881",
		speedStart: time.Now().Add(-10 * time.Second),
		downEMA:    newEMASpeed(),
		upEMA:      newEMASpeed(),
	}
	defer c.Close()

	// Populate cumulative counters with totals that would give the
	// fallback path an obviously different answer: 10 MB / 10 s = 1 MB/s
	// for Speed(), 5 MB / 10 s = 500 kB/s for UploadSpeed().
	c.downloaded.Store(10_000_000)
	c.uploaded.Store(5_000_000)

	// Feed the EMAs at a distinct low rate and wait enough for at least
	// one EMA tick (1 s) so .rate() leaves zero.
	c.downEMA.add(100_000)
	c.upEMA.add(50_000)
	time.Sleep(1200 * time.Millisecond)

	snap := c.Snapshot(0)

	if snap.DownRate != float64(c.downEMA.rate()) {
		t.Errorf("DownRate should equal downEMA.rate(): got %.0f, ema %d", snap.DownRate, c.downEMA.rate())
	}
	if snap.UpRate != float64(c.upEMA.rate()) {
		t.Errorf("UpRate should equal upEMA.rate(): got %.0f, ema %d", snap.UpRate, c.upEMA.rate())
	}
	// Sanity check: the EMA-backed rates must differ from the cumulative
	// fallbacks, otherwise we wouldn't know the new code path is live.
	if snap.DownRate == c.Speed() {
		t.Errorf("DownRate unexpectedly matches cumulative Speed(): %.0f", snap.DownRate)
	}
	if snap.UpRate == c.UploadSpeed() {
		t.Errorf("UpRate unexpectedly matches cumulative UploadSpeed(): %.0f", snap.UpRate)
	}
}

// TestSnapshotFallsBackToSpeedWhenNoEMA keeps the zero-value-Client contract
// for tests that construct &Client{} directly.
func TestSnapshotFallsBackToSpeedWhenNoEMA(t *testing.T) {
	c := &Client{
		Addr:       "1.2.3.4:6881",
		speedStart: time.Now().Add(-5 * time.Second),
	}
	c.downloaded.Store(5000) // 1000 B/s over 5 s
	c.uploaded.Store(2500)   // 500 B/s over 5 s

	snap := c.Snapshot(0)
	if snap.DownRate < 900 || snap.DownRate > 1100 {
		t.Errorf("DownRate fallback: got %.0f, want ~1000", snap.DownRate)
	}
	if snap.UpRate < 400 || snap.UpRate > 600 {
		t.Errorf("UpRate fallback: got %.0f, want ~500", snap.UpRate)
	}
}
