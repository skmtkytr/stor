package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUICacheControlHeader verifies that the UI handler attaches
// Cache-Control + ETag to /_app/ paths and the root index but leaves
// API endpoints untouched. The middleware exists because we serve
// un-hashed filenames under /_app/immutable/ and need browsers to
// revalidate on every load instead of trusting their heuristic cache.
func TestUICacheControlHeader(t *testing.T) {
	cases := []struct {
		path     string
		wantCC   string
		wantETag bool
	}{
		{"/", "no-cache, must-revalidate", true},
		{"/index.html", "no-cache, must-revalidate", true},
		{"/_app/immutable/entry/app.js", "no-cache, must-revalidate", true},
		{"/_app/immutable/chunks/client.js", "no-cache, must-revalidate", true},
		{"/_app/version.json", "no-cache, must-revalidate", true},
		{"/api/rpc", "", false},
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := uiCacheControl(inner)

	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if got := rr.Header().Get("Cache-Control"); got != c.wantCC {
			t.Errorf("path=%s: Cache-Control = %q, want %q", c.path, got, c.wantCC)
		}
		gotETag := rr.Header().Get("ETag") != ""
		if gotETag != c.wantETag {
			t.Errorf("path=%s: ETag present = %v, want %v", c.path, gotETag, c.wantETag)
		}
	}
}

// TestUICacheControlRevalidate verifies the 304 Not Modified path: when a
// client returns the current ETag in If-None-Match, the middleware short-
// circuits with 304 and does not invoke the inner handler. This is the
// hot path for repeated reloads and must avoid sending the body.
func TestUICacheControlRevalidate(t *testing.T) {
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerCalled = true
		_, _ = w.Write([]byte("body"))
	})
	h := uiCacheControl(inner)

	req := httptest.NewRequest("GET", "/_app/immutable/entry/app.js", nil)
	req.Header.Set("If-None-Match", uiETag)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("body length = %d, want 0", rr.Body.Len())
	}
	if innerCalled {
		t.Error("inner handler should not be invoked for cache hit")
	}

	// Mismatched If-None-Match must fall through to the inner handler.
	innerCalled = false
	req2 := httptest.NewRequest("GET", "/_app/immutable/entry/app.js", nil)
	req2.Header.Set("If-None-Match", `W/"stale-etag"`)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if !innerCalled {
		t.Error("inner handler must be invoked when ETag does not match")
	}
	if rr2.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr2.Code)
	}
}
