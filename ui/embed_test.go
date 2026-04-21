package ui

import (
	"io/fs"
	"regexp"
	"testing"
)

// TestEmbedIncludesUnderscoreFiles is a regression guard for the
// //go:embed directive. The default behaviour of `dist/*` is to
// EXCLUDE files starting with `_` or `.` during directory recursion,
// which silently drops Vite chunks whose base64 hash happens to start
// with `_` (e.g. `_pXqBzEX.js`). When that happens, index.html
// references the chunk but the embedded FS doesn't contain it →
// 404 in the browser.
//
// The fix is `//go:embed all:dist`. To make sure nobody reverts that
// without noticing, we keep a deliberate `_sentinel.txt` under
// ui/frontend/static/embed_test/. SvelteKit copies it through to
// dist/, and this test asserts it is reachable from the embed FS.
// With the old `dist/*` directive this read fails; with `all:dist` it
// passes.
func TestEmbedIncludesUnderscoreFiles(t *testing.T) {
	data, err := fs.ReadFile(FS, "dist/embed_test/_sentinel.txt")
	if err != nil {
		t.Fatalf("expected dist/embed_test/_sentinel.txt to be embedded, got %v\n"+
			"most likely //go:embed lost its `all:` prefix and is silently "+
			"excluding _-prefixed files (Vite chunk hashes can start with `_`).", err)
	}
	if len(data) == 0 {
		t.Errorf("sentinel is empty; expected non-empty content")
	}
}

// TestEmbedAssetsReferencedByIndexExist parses index.html and verifies
// every /_app/... asset URL it references resolves in the embed FS.
// This catches the `_*` chunk regression at runtime against whatever
// the latest build produced — even without the deliberate sentinel.
func TestEmbedAssetsReferencedByIndexExist(t *testing.T) {
	html, err := fs.ReadFile(FS, "dist/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}

	// Match `/_app/.../filename.{js,css,woff2,...}` references.
	re := regexp.MustCompile(`/_app/[A-Za-z0-9_./-]+\.[A-Za-z0-9]+`)
	matches := re.FindAllString(string(html), -1)
	if len(matches) == 0 {
		t.Skip("no /_app/... asset references found in index.html (build may have changed)")
	}

	seen := make(map[string]bool)
	for _, m := range matches {
		if seen[m] {
			continue
		}
		seen[m] = true
		// Strip leading slash, prefix with "dist".
		path := "dist" + m
		if _, err := fs.Stat(FS, path); err != nil {
			t.Errorf("index.html references %s but it is not in the embedded FS: %v", m, err)
		}
	}
}
