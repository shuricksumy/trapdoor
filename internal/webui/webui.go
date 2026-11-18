// Package webui serves the embedded single-page app built from web/.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// dist is filled by `make ui` (cd web && npm run build). It always contains
// .gitkeep, so the package builds without Node; then a notice is served.
//
//go:embed all:dist
var dist embed.FS

var inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// Handler serves the UI: hashed assets with long caching, every other path
// gets index.html (client-side routing), all with a strict CSP.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The web UI is not built into this binary. Run `make ui build`, or use the API under /api/v1.\n"))
		})
	}
	csp := policy(index)
	files := http.FileServerFS(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		p := path.Clean("/" + r.URL.Path)
		if p != "/" && p != "/index.html" {
			if f, err := root.Open(strings.TrimPrefix(p, "/")); err == nil {
				st, _ := f.Stat()
				_ = f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "/assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
			if path.Ext(p) != "" { // a missing file, not an app route
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time0, bytes.NewReader(index))
	})
}

// policy allows only same-origin resources, plus the inline theme script in
// index.html by its hash (it must run before first paint).
func policy(index []byte) string {
	scripts := []string{"'self'"}
	for _, m := range inlineScript.FindAllSubmatch(index, -1) {
		sum := sha256.Sum256(m[1])
		scripts = append(scripts, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scripts, " "),
		"style-src 'self' 'unsafe-inline'", // Svelte sets inline style attributes
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}
