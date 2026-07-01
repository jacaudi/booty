package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestUI() http.Handler {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>Booty</title>")},
		"assets/app.js": {Data: []byte("console.log('booty')")},
	}
	return http.StripPrefix("/ui/", uiHandler(fsys))
}

func TestUIHandler_ServesIndexAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestUI().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("body = %q, want index.html", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
}

func TestUIHandler_ServesRealAsset(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestUI().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q, want javascript", ct)
	}
}

func TestUIHandler_ClientRouteFallsBackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestUI().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/hosts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("client route should serve index.html, got %q", rec.Body.String())
	}
}

func TestUIHandler_MissingAssetReturns404(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestUI().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/assets/missing.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (missing asset must not serve the SPA shell)", rec.Code)
	}
}

func TestUIHandler_DirectoryPathFallsBackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestUI().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/assets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (directory path must serve SPA shell, not a listing or redirect)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("body = %q, want index.html for directory path", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "app.js") {
		t.Fatalf("body must not contain directory listing content, got %q", rec.Body.String())
	}
}
