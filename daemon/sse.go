package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/skmtkytr/stor/events"
)

// sseKeepaliveInterval is how often we send a comment line to keep proxies
// from closing idle SSE connections. 30s is below the default idle timeout of
// most reverse proxies (nginx 60s, ALB 60s).
const sseKeepaliveInterval = 30 * time.Second

// sseSubscriberBuffer is the per-connection event channel buffer. An SSE
// client that briefly stalls (TCP backpressure) won't drop events as long as
// fewer than this many accumulate.
const sseSubscriberBuffer = 512

// handleEvents serves Server-Sent Events to a single client. Format follows
// the W3C EventSource spec: "event: <type>\nid: <unix nano>\ndata: <json>\n\n".
//
// Query params:
//
//	types=peer.connected,tracker.reply  comma-separated allow-list (optional)
//
// The connection persists until the client disconnects (r.Context().Done())
// or the engine bus is closed.
func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	// http.NewResponseController unwraps middleware-wrapped writers and
	// forwards Flush/SetWriteDeadline to the underlying connection. We use
	// it instead of a `w.(http.Flusher)` cast because logMiddleware wraps
	// the writer in a struct that does not implement Flusher directly.
	rc := http.NewResponseController(w)

	// Disable the global server WriteTimeout for this long-lived stream.
	// Errors are non-fatal: a writer that doesn't support deadlines (e.g.
	// httptest.ResponseRecorder) just keeps its zero deadline.
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx response buffering
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		// If we can't flush after WriteHeader, the connection cannot serve
		// SSE. The status code has already been written, so we just bail.
		return
	}

	filter := buildTypeFilter(r.URL.Query().Get("types"))

	sub := d.engine.Bus().Subscribe(r.Context(), events.SubscribeOptions{
		Buffer: sseSubscriberBuffer,
		Filter: filter,
		Name:   "sse#" + r.RemoteAddr,
	})

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			// Bus closed (engine shutdown). The subscription ctx-watcher
			// also ends the sub on r.Context().Done(), so this case fires
			// only when the bus terminates while a client is connected.
			return
		case ev := <-sub.C:
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeSSEEvent renders a single event in EventSource frame format. The data
// payload is a single-line JSON encoding of the full Event struct (so clients
// see the type, time, torrent_id, and payload together).
func writeSSEEvent(w http.ResponseWriter, ev events.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		// A marshal failure here is a programming bug (Event has no
		// channel/func fields), but don't kill the whole connection —
		// emit a comment so the client knows we skipped one.
		_, werr := fmt.Fprintf(w, ": marshal error: %s\n\n", err)
		return werr
	}
	_, err = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n",
		ev.Type, ev.Time.UnixNano(), data)
	return err
}

// buildTypeFilter parses a comma-separated list of event types into a filter
// function for events.SubscribeOptions. Returns nil (no filtering) when the
// input is empty.
func buildTypeFilter(raw string) func(events.Event) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	allowed := make(map[events.Type]struct{})
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		allowed[events.Type(t)] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil
	}
	return func(ev events.Event) bool {
		_, ok := allowed[ev.Type]
		return ok
	}
}
