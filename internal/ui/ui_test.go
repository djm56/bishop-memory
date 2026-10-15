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
	for name, h := range map[string]gin.HandlerFunc{"triage": TriagePageHandler(), "missions": MissionsPageHandler(), "performance": PerformancePageHandler()} {
		page := string(serve(t, h).Body.Bytes())
		for _, want := range []string{`href="/favicon.svg"`, `href="/favicon.ico"`} {
			if !strings.Contains(page, want) {
				t.Fatalf("%s page does not link %s", name, want)
			}
		}
	}
}

// TestPagesLinkEachOther checks every page carries the page switch, so the
// operator can move between triage, the mission HUD and performance.
func TestPagesLinkEachOther(t *testing.T) {
	for name, h := range map[string]gin.HandlerFunc{"triage": TriagePageHandler(), "missions": MissionsPageHandler(), "performance": PerformancePageHandler()} {
		rec := serve(t, h)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s page: status %d, type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		page := rec.Body.String()
		for _, want := range []string{`href="/triage"`, `href="/missions"`, `href="/performance"`} {
			if !strings.Contains(page, want) {
				t.Fatalf("%s page has no link %s", name, want)
			}
		}
	}
}

// TestMissionsPageRendersDirectiveStatus guards the HUD's use of a
// directive's status: the directive card must render its pill from the
// directive's status (defaulting to active), and the under-review pill must be
// styled, or a reopened directive renders identically to an active one. The
// pillFor(x.status || 'active') needle appears only in the directive card.
func TestMissionsPageRendersDirectiveStatus(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	for _, want := range []string{"pillFor(x.status || 'active')", ".pill.under-review", ".pill.active"} {
		if !strings.Contains(page, want) {
			t.Fatalf("missions page does not contain %q", want)
		}
	}
}

// TestTriagePageRendersStaleRunsUI is a source-text check only: it cannot run
// the page's JavaScript, so it does not prove the handlers behave. It asserts
// that the reopen and operator-close endpoints, the stale pill styling, the
// amendment label and the data-amends detection are present in the served
// source, and that one known-broken expression has not returned.
func TestTriagePageRendersStaleRunsUI(t *testing.T) {
	page := serve(t, TriagePageHandler()).Body.String()
	needles := []string{
		`/v1/directive-proposals/${b.dataset.reopen}/reopen`,
		"/v1/triage/runs/${run}/operator-close",
		".pill.stale { color:var(--warn) }",
		"Ratify as amendment to",
		"data-amends=",
		"card.dataset.amends",
		`data-close="failed"`,
		"b.dataset.close",
	}
	for _, needle := range needles {
		if !strings.Contains(page, needle) {
			t.Fatalf("triage page does not contain %q", needle)
		}
	}
	// Regression: the card is the data-pid element, so searching its
	// descendants returned null and threw after the server had ratified.
	if strings.Contains(page, "card.querySelector('[data-pid]')") {
		t.Fatal("triage page contains the broken card.querySelector('[data-pid]') expression")
	}
}

// TestMissionsPageRendersDebriefSections is a source-text check only: it cannot
// run the page's JavaScript. It asserts that the mission page reads the HUD's
// deliverables and agent_mistakes fields with an empty-array fallback, renders a
// section for each, passes every rendered string field through esc(), shows —
// for a NULL path, and words each empty state for a debrief that is present,
// missing on a complete mission, or not yet written.
func TestMissionsPageRendersDebriefSections(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	needles := []string{
		"d.deliverables || []",
		"d.agent_mistakes || []",
		"<h3>Deliverables ",
		"<h3>Agent mistakes ",
		"x.path ? esc(x.path) : '—'",
		"esc(x.note)",
		"esc(x.mistake)",
		"'@' + esc(x.agent)",
		"esc(x.agent_raw || '—')",
		"impact ${esc(x.impact)}",
		"esc(x.impact_note)",
		"esc(x.correction)",
		"esc(x.prevention)",
		"'No deliverables listed in the debrief.' : m.status === 'complete' ? 'No debrief imported.'",
		"'No agent mistakes recorded in the debrief.' : m.status === 'complete' ? 'No debrief imported.'",
	}
	for _, needle := range needles {
		if !strings.Contains(page, needle) {
			t.Errorf("missions page does not contain %q", needle)
		}
	}
}
