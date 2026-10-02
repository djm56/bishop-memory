package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func serve(t *testing.T, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	return rec
}

// TestIconsAreServedWithTheirTypes checks each icon route returns the
// embedded bytes with a content type a browser will render, so a broken or
// empty embed fails here rather than as a blank tab icon.
func TestIconsAreServedWithTheirTypes(t *testing.T) {
	cases := []struct {
		name    string
		handler gin.HandlerFunc
		ctype   string
		magic   []byte
	}{
		{"favicon.svg", FaviconSVGHandler(), "image/svg+xml", []byte("<svg")},
		{"favicon.ico", FaviconICOHandler(), "image/png", []byte("\x89PNG\r\n\x1a\n")},
	}
	for _, tc := range cases {
		rec := serve(t, tc.handler)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.ctype {
			t.Fatalf("%s: Content-Type %q, want %q", tc.name, got, tc.ctype)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), tc.magic) {
			t.Fatalf("%s: body does not start with %q", tc.name, tc.magic)
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Fatalf("%s: no Cache-Control header", tc.name)
		}
	}
}

// TestPageLinksBothIcons guards the <head> links: without them a browser
// falls back to requesting /favicon.ico alone and ignores the SVG.
func TestPageLinksBothIcons(t *testing.T) {
	page := string(serve(t, TriagePageHandler()).Body.Bytes())
	for _, want := range []string{`href="/favicon.svg"`, `href="/favicon.ico"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("triage page does not link %s", want)
		}
	}
}
