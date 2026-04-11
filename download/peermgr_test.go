package download

import (
	"testing"
	"time"
)

func newTestClient(addr string, downloaded int64) *Client {
	c := &Client{
		Addr:        addr,
		choking:     false,
		speedStart:  time.Now().Add(-10 * time.Second),
		maxPipeline: DefaultMaxPipeline,
	}
	c.downloaded.Store(downloaded)
	return c
}

func TestPeerManagerRegisterUnregister(t *testing.T) {
	pm := NewPeerManager(4)
	c1 := newTestClient("1.1.1.1:6881", 0)
	c2 := newTestClient("2.2.2.2:6881", 0)

	pm.Register(c1)
	pm.Register(c2)
	if pm.PeerCount() != 2 {
		t.Fatalf("expected 2 peers, got %d", pm.PeerCount())
	}

	pm.Unregister(c1)
	if pm.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", pm.PeerCount())
	}

	pm.Unregister(c2)
	if pm.PeerCount() != 0 {
		t.Fatalf("expected 0 peers, got %d", pm.PeerCount())
	}
}

func TestPeerManagerUnregisterNonexistent(t *testing.T) {
	pm := NewPeerManager(4)
	c := newTestClient("1.1.1.1:6881", 0)
	pm.Unregister(c) // should not panic
}

func TestRechokeRanks(t *testing.T) {
	pm := NewPeerManager(2)

	peers := []*Client{
		newTestClient("slow1", 100),
		newTestClient("fast1", 10000),
		newTestClient("medium", 5000),
		newTestClient("fast2", 9000),
		newTestClient("slow2", 200),
	}

	for _, c := range peers {
		c.choking = true
		pm.Register(c)
	}

	p := newTestClient("test", 10000)
	speed := p.Speed()
	if speed < 900 || speed > 1100 {
		t.Fatalf("expected ~1000 bytes/s, got %.1f", speed)
	}
}

func TestClientSpeed(t *testing.T) {
	c := &Client{
		speedStart: time.Now().Add(-5 * time.Second),
	}
	c.downloaded.Store(5000)

	speed := c.Speed()
	if speed < 900 || speed > 1100 {
		t.Fatalf("expected ~1000 bytes/s, got %.1f", speed)
	}
}

func TestClientSpeedReset(t *testing.T) {
	c := &Client{
		speedStart: time.Now().Add(-5 * time.Second),
	}
	c.downloaded.Store(5000)

	c.ResetSpeed()

	if c.downloaded.Load() != 0 {
		t.Fatalf("expected downloaded reset to 0, got %d", c.downloaded.Load())
	}
	if c.lastSpeed.Load() < 900 || c.lastSpeed.Load() > 1100 {
		t.Fatalf("expected lastSpeed ~1000, got %d", c.lastSpeed.Load())
	}
}

func TestDefaultUnchokeSlots(t *testing.T) {
	pm := NewPeerManager(0)
	if pm.unchokeSlots != DefaultUnchokeSlots {
		t.Fatalf("expected %d, got %d", DefaultUnchokeSlots, pm.unchokeSlots)
	}
}
