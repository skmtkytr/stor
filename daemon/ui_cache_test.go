package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUICacheControlHeader verifies that the UI handler attaches
// Cache-Control: no-cache, must-revalidate to /_app/ paths and the root
// index. The middleware exists because we serve un-hashed filenames
// under /_app/immutable/ and need browsers to revalidate on every load
// instead of trusting their heuristic cache.
func TestUICacheControlHeader(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/", "no-cache, must-revalidate"},
		{"/index.html", "no-cache, must-revalidate"},
		{"/_app/immutable/entry/app.js", "no-cache, must-revalidate"},
		{"/_app/immutable/chunks/client.js", "no-cache, must-revalidate"},
		{"/_app/version.json", "no-cache, must-revalidate"},
		{"/api/rpc", ""}, // not /_app/, no header
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := uiCacheControl(inner)

	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		got := rr.Header().Get("Cache-Control")
		if got != c.want {
			t.Errorf("path=%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}
