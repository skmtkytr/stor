package daemon

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skmtkytr/stor/events"
)

// readSSEFrame reads from the body until it sees a complete SSE frame
// (terminated by a blank line) or hits a deadline. Each call returns one
// frame's raw text including its trailing "\n\n". Comment-only frames
// (e.g. ": keepalive\n\n") are skipped automatically.
func readSSEFrame(t *testing.T, br *bufio.Reader, deadline time.Time) string {
	t.Helper()
	var sb strings.Builder
	for {
		if time.Now().After(deadline) {
			t.Fatalf("readSSEFrame: deadline exceeded; got so far: %q", sb.String())
		}
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF && sb.Len() == 0 {
				return ""
			}
			t.Fatalf("readSSEFrame: %v (partial: %q)", err, sb.String())
		}
		sb.WriteString(line)
		if line == "\n" || line == "\r\n" {
			frame := sb.String()
			// Pure-comment frame: a single line starting with ":" plus the
			// terminator. Skip it so callers always see a real event.
			trimmed := strings.TrimRight(frame, "\n")
			if strings.HasPrefix(trimmed, ":") && !strings.Contains(trimmed, "\n") {
				sb.Reset()
				continue
			}
			return frame
		}
	}
}

// startSSETestServer brings up a daemon-backed SSE server. Returns the
// daemon (so tests can publish events directly on its bus), the test
// server, and the bearer auth header value.
func startSSETestServer(t *testing.T) (*Daemon, *httptest.Server, string) {
	t.Helper()
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})
	// Install hooks the same way Start would; tests don't call Start.
	d.installEventHooks()
	t.Cleanup(d.uninstallEventHooks)

	ts := httptest.NewServer(d.server.Handler)
	t.Cleanup(ts.Close)
	return d, ts, "Bearer " + cfg.APIKey
}

func TestSSEDeliversEvents(t *testing.T) {
	d, ts, auth := startSSETestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)

	// We can't tell from the client when the server's Subscribe call has
	// completed, so publish on a tick until the test reads its first frame.
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.engine.Bus().Publish(events.Event{
					Type:      events.TypeTorrentAdded,
					TorrentID: "abc",
					Payload:   events.TorrentAddedPayload{Source: "test", Name: "alpha"},
				})
			}
		}
	}()

	frame := readSSEFrame(t, br, time.Now().Add(3*time.Second))
	if !strings.Contains(frame, "event: torrent.added") {
		t.Errorf("missing event line in frame:\n%s", frame)
	}
	if !strings.Contains(frame, "data: {") {
		t.Errorf("missing data line in frame:\n%s", frame)
	}
	if !strings.Contains(frame, `"torrent_id":"abc"`) {
		t.Errorf("missing torrent_id payload in frame:\n%s", frame)
	}
	if !strings.Contains(frame, `"type":"torrent.added"`) {
		t.Errorf("missing type field in frame data:\n%s", frame)
	}
	if !strings.Contains(frame, "id: ") {
		t.Errorf("missing id line in frame:\n%s", frame)
	}
}

func TestSSEFilter(t *testing.T) {
	d, ts, auth := startSSETestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/events?types=tracker.reply", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)

	// Publish both the unwanted and the wanted type repeatedly. The
	// filtered subscription should only see the wanted one.
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.engine.Bus().Publish(events.Event{Type: events.TypeTorrentAdded, TorrentID: "ignored"})
				d.engine.Bus().Publish(events.Event{
					Type: events.TypeAnnounceReply,
					Payload: events.AnnounceReplyPayload{
						Tracker: "http://example.com", NumPeers: 3,
					},
				})
			}
		}
	}()

	frame := readSSEFrame(t, br, time.Now().Add(3*time.Second))
	if !strings.Contains(frame, "event: tracker.reply") {
		t.Errorf("first frame should be tracker.reply:\n%s", frame)
	}
	if strings.Contains(frame, "torrent.added") {
		t.Errorf("filter leaked torrent.added:\n%s", frame)
	}

	// Read a few more frames; none should be torrent.added.
	for range 5 {
		f := readSSEFrame(t, br, time.Now().Add(2*time.Second))
		if strings.Contains(f, "torrent.added") {
			t.Errorf("filter leaked torrent.added in subsequent frame:\n%s", f)
		}
		if !strings.Contains(f, "tracker.reply") {
			t.Errorf("subsequent frame missing tracker.reply:\n%s", f)
		}
	}
}

func TestSSECtxCancelCleansUp(t *testing.T) {
	_, ts, auth := startSSETestServer(t)

	// Warm up: open & close one connection so any one-shot init happens
	// before we sample the baseline goroutine count.
	{
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", http.NoBody)
		req.Header.Set("Authorization", auth)
		if resp, err := ts.Client().Do(req); err == nil {
			_ = resp.Body.Close()
		}
		cancel()
		time.Sleep(50 * time.Millisecond)
	}

	before := runtime.NumGoroutine()

	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", http.NoBody)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		resp, err := ts.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
		cancel()
	}

	// Give the server-side handlers a moment to unwind and the bus's
	// per-subscription cleanup goroutine to drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	after := runtime.NumGoroutine()
	// Allow some slack: GC, idle conn pool, etc. The point is we shouldn't
	// see ~5 leaked SSE handlers and ~5 leaked Subscribe-cleanup goroutines.
	if after > before+5 {
		t.Errorf("goroutine leak suspected: before=%d after=%d", before, after)
	}
}

func TestSSEAuthRequired(t *testing.T) {
	_, ts, _ := startSSETestServer(t)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want 401", resp.StatusCode)
	}
}
