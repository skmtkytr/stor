package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skmtkytr/stor/events"
)

// TestMetricsEventCountersInstalled verifies the daemon's onPublish hook
// drives stor_events_published_total.
func TestMetricsEventCountersInstalled(t *testing.T) {
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})
	d.installEventHooks()
	t.Cleanup(d.uninstallEventHooks)

	// Publish a few events of one type and one of another.
	for range 3 {
		d.engine.Bus().Publish(events.Event{Type: events.TypeTorrentAdded, TorrentID: "t1"})
	}
	d.engine.Bus().Publish(events.Event{Type: events.TypeAnnounceReply})

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, want := range []string{
		`# HELP stor_events_published_total`,
		`# TYPE stor_events_published_total counter`,
		`stor_events_published_total{type="torrent.added"} 3`,
		`stor_events_published_total{type="tracker.reply"} 1`,
		`# HELP stor_events_dropped_total`,
		`# TYPE stor_events_dropped_total counter`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body)
		}
	}
}

// TestMetricsEventCountersDropped verifies the onDrop hook drives
// stor_events_dropped_total when a slow subscriber's buffer overflows.
func TestMetricsEventCountersDropped(t *testing.T) {
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})
	d.installEventHooks()
	t.Cleanup(d.uninstallEventHooks)

	// Subscribe with a tiny buffer and never drain. The first event lands,
	// every subsequent publish should increment the drop counter.
	bus := d.engine.Bus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, events.SubscribeOptions{
		Buffer: 1,
		Name:   "slow-test-sub",
	})
	_ = sub

	// One that fits (buffered), then ten that should drop.
	bus.Publish(events.Event{Type: events.TypeTorrentAdded})
	for range 10 {
		bus.Publish(events.Event{Type: events.TypeTorrentAdded})
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d", w.Code)
	}
	body := w.Body.String()

	if !strings.Contains(body, `stor_events_dropped_total{subscriber="slow-test-sub"}`) {
		t.Errorf("missing dropped sample in body:\n%s", body)
	}
	// Published counter should reflect all 11 emissions of torrent.added.
	if !strings.Contains(body, `stor_events_published_total{type="torrent.added"} 11`) {
		t.Errorf("expected 11 published torrent.added; body:\n%s", body)
	}
}

// TestMetricsEventCountersUninstall verifies that after uninstallEventHooks
// further publishes do not increment counters.
func TestMetricsEventCountersUninstall(t *testing.T) {
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})
	d.installEventHooks()

	d.engine.Bus().Publish(events.Event{Type: events.TypeTorrentAdded})
	d.uninstallEventHooks()
	d.engine.Bus().Publish(events.Event{Type: events.TypeTorrentAdded})
	d.engine.Bus().Publish(events.Event{Type: events.TypeTorrentAdded})

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `stor_events_published_total{type="torrent.added"} 1`) {
		t.Errorf("counter incremented after uninstall; body:\n%s", body)
	}
}
