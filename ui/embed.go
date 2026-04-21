package ui

import "embed"

// The `all:` prefix is essential. Without it, //go:embed silently drops
// any file or directory whose name starts with `_` or `.` during
// recursion — and Vite chunk hashes are base64, so a build can produce
// chunks like `_pXqBzEX.js`. The HTML references those chunks but the
// embedded FS doesn't contain them, leading to 404s in the browser.
// See ui/embed_test.go for the regression guard.
//
//go:embed all:dist
var FS embed.FS
