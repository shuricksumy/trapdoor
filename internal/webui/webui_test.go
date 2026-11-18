package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyHashesInlineScripts(t *testing.T) {
	js := "document.documentElement.dataset.theme='dark';"
	csp := policy([]byte("<html><script>" + js + "</script><script type=\"module\" src=\"/x.js\"></script></html>"))
	sum := sha256.Sum256([]byte(js))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(csp, "script-src 'self' "+want) {
		t.Errorf("csp = %s, want hash %s", csp, want)
	}
	for _, d := range []string{"frame-ancestors 'none'", "object-src 'none'", "default-src 'self'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("csp missing %q", d)
		}
	}
}

func TestHandler(t *testing.T) {
	h := Handler()
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	idx := get("/")
	if idx.Code == http.StatusServiceUnavailable {
		t.Skip("UI not built (run make ui)")
	}
	if idx.Code != http.StatusOK || !strings.Contains(idx.Body.String(), `<div id="app">`) {
		t.Fatalf("index = %d", idx.Code)
	}
	if idx.Header().Get("Content-Security-Policy") == "" || idx.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("index headers = %v", idx.Header())
	}
	// Client-side routes get index.html; missing files 404.
	if rec := get("/certificates"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="app"`) {
		t.Errorf("/certificates = %d", rec.Code)
	}
	if rec := get("/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d", rec.Code)
	}
	if rec := get("/favicon.svg"); rec.Code != http.StatusOK {
		t.Errorf("favicon = %d", rec.Code)
	}
}
