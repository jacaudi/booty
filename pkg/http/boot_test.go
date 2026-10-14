package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/fstest"
)

// Ported from jeefy/booty pkg/server/http_test.go @ 639de71 (MIT).

func bootTestFiles() fstest.MapFS {
	return fstest.MapFS{
		"undionly.kpxe": {Data: []byte("BIOS-IPXE")},
		"ipxe.efi":      {Data: []byte("EFI-IPXE")},
		"snponly.efi":   {Data: []byte("SNP-IPXE")},
	}
}

// bootDo sends a request whose URL.Path is set verbatim, so the client does
// not clean it, straight to the handler.
func bootDo(t *testing.T, h http.Handler, method, p string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
	req.URL.Path = p
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBootHandler(t *testing.T) {
	h := withBootRoute(bootTestFiles(), http.NotFoundHandler())

	for name, want := range map[string][2]string{
		"undionly.kpxe": {"BIOS-IPXE", "application/octet-stream"},
		"ipxe.efi":      {"EFI-IPXE", "application/efi"},
		"snponly.efi":   {"SNP-IPXE", "application/efi"},
	} {
		r := bootDo(t, h, http.MethodGet, "/boot/"+name)
		if r.Code != http.StatusOK || r.Body.String() != want[0] {
			t.Errorf("/boot/%s: status %d body %q", name, r.Code, r.Body.String())
		}
		if got := r.Header().Get("Content-Type"); got != want[1] {
			t.Errorf("/boot/%s: content-type %q, want %q", name, got, want[1])
		}
		if got := r.Header().Get("Content-Length"); got != strconv.Itoa(len(want[0])) {
			t.Errorf("/boot/%s: content-length %q", name, got)
		}
		if got := r.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("/boot/%s: cache-control %q", name, got)
		}
	}

	head := bootDo(t, h, http.MethodHead, "/boot/ipxe.efi")
	headLen := head.Header().Get("Content-Length")
	if head.Code != http.StatusOK || headLen != "8" || head.Body.Len() != 0 {
		t.Errorf("HEAD /boot/ipxe.efi: status %d length %q body %q", head.Code, headLen, head.Body.String())
	}
	if got := head.Header().Get("Content-Type"); got != "application/efi" {
		t.Errorf("HEAD content-type %q", got)
	}

	for _, p := range []string{"/boot/", "/boot/pxelinux.0", "/boot/../boot/ipxe.efi/x", "/boot/ipxe.efi/", "/boot/booty.ipxe", "/boot/../ignition.json"} {
		if r := bootDo(t, h, http.MethodGet, p); r.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, r.Code)
		}
	}
	if r := bootDo(t, h, http.MethodPost, "/boot/ipxe.efi"); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /boot/ipxe.efi: status %d, want 405", r.Code)
	}

	if r := bootDo(t, bootFileHandler(nil), http.MethodGet, "/boot/ipxe.efi"); r.Code != http.StatusNotFound {
		t.Errorf("nil files: status %d, want 404", r.Code)
	}
}

func TestBootHandlerCollapsesSlashesWithoutRedirect(t *testing.T) {
	h := withBootRoute(bootTestFiles(), http.NotFoundHandler())
	for _, p := range []string{"/boot//ipxe.efi", "/boot///ipxe.efi", "/boot/./ipxe.efi"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := bootDo(t, h, method, p)
			if r.Code != http.StatusOK || r.Header().Get("Content-Length") != "8" {
				t.Errorf("%s %s: status %d length %q (location %q)",
					method, p, r.Code, r.Header().Get("Content-Length"), r.Header().Get("Location"))
			}
			if method == http.MethodGet {
				if body, _ := io.ReadAll(r.Body); string(body) != "EFI-IPXE" {
					t.Errorf("GET %s: body %q", p, body)
				}
			}
		}
	}
}

func TestWithBootRoutePassesOtherPathsThrough(t *testing.T) {
	h := withBootRoute(bootTestFiles(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	if r := bootDo(t, h, http.MethodGet, "/ignition.json"); r.Code != http.StatusTeapot {
		t.Errorf("status %d, want next handler (418)", r.Code)
	}
}
