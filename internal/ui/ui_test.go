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
	for name, h := range map[string]gin.HandlerFunc{"triage": TriagePageHandler(), "missions": MissionsPageHandler(), "performance": PerformancePageHandler(), "crew": CrewPageHandler()} {
		page := string(serve(t, h).Body.Bytes())
		for _, want := range []string{`href="/favicon.svg"`, `href="/favicon.ico"`} {
			t.Run(name+"/"+want, func(t *testing.T) {
				if !strings.Contains(page, want) {
					t.Fatalf("%s page does not link %s", name, want)
				}
			})
		}
	}
}

// TestPagesLinkEachOther checks every page carries the page switch, so the
// operator can move between triage, the mission HUD, performance and crew.
func TestPagesLinkEachOther(t *testing.T) {
	for name, h := range map[string]gin.HandlerFunc{"triage": TriagePageHandler(), "missions": MissionsPageHandler(), "performance": PerformancePageHandler(), "crew": CrewPageHandler()} {
		rec := serve(t, h)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s page: status %d, type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		page := rec.Body.String()
		for _, want := range []string{`href="/triage"`, `href="/missions"`, `href="/performance"`, `href="/crew"`} {
			t.Run(name+"/"+want, func(t *testing.T) {
				if !strings.Contains(page, want) {
					t.Fatalf("%s page has no link %s", name, want)
				}
			})
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
		"'No deliverables listed in the debrief.' : m.status === 'complete' ? 'No debrief imported.' : 'Written when the mission closes.'",
		"'No agent mistakes recorded in the debrief.' : m.status === 'complete' ? 'No debrief imported.' : 'Written when the mission closes.'",
	}
	for _, needle := range needles {
		if !strings.Contains(page, needle) {
			t.Errorf("missions page does not contain %q", needle)
		}
	}
}

// TestMissionsPageRendersLessonsSection is a source-text check only: it cannot
// run the page's JavaScript. It asserts that the mission page reads the HUD's
// lessons field with an empty-array fallback, groups it by kind (wrong
// assumptions, the reuse trigger, the reuse steps), renders a Lessons section,
// passes every rendered string through esc(), and words the empty state for a
// debrief that is present, missing on a complete mission, or not yet written.
func TestMissionsPageRendersLessonsSection(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	needles := []string{
		"d.lessons || []",
		"<h3>Lessons ",
		`less.filter(x => x.kind === 'wrong-assumption')`,
		`less.find(x => x.kind === 'similar-mission')`,
		`less.filter(x => x.kind === 'reuse-step')`,
		`<div class="target">${esc(w.text)}</div>`,
		"esc(w.why)",
		"esc(w.correction)",
		"esc(trig.text)",
		"<li>${esc(st.text)}</li>",
		`'No lessons recorded in the debrief.'`,
		`'No lessons recorded in the debrief.' : m.status === 'complete' ? 'No debrief imported.' : 'Written when the mission closes.'`,
	}
	for _, needle := range needles {
		if !strings.Contains(page, needle) {
			t.Errorf("missions page does not contain %q", needle)
		}
	}
}

// TestCrewPageRendersMetrics is a source-text check only: it cannot run the
// page's JavaScript, so it does not prove the page renders correctly. It
// asserts that the served source contains each listed expression (the fetch of
// /v1/crew, the assignment of each rendered field, the dash fallbacks for role,
// first-pass approval, average grade and last active, the `|| {}` guards on
// mistakes and service_records, the re-reviews title, the empty-state text and
// the aria-current marker), and
// that the string "innerHTML" appears nowhere in the crew page. The innerHTML
// absence check is paired with a control: the missions page, served the same
// way, does contain "innerHTML", so a pass means the page was searched rather
// than that nothing was served.
func TestCrewPageRendersMetrics(t *testing.T) {
	page := serve(t, CrewPageHandler()).Body.String()
	needles := []string{
		"api('/v1/crew')",
		"nameDiv.textContent = agent.agent",
		"roleDiv.textContent = agent.role || '—'",
		"agent.steps_run + ' / ' + agent.steps_total",
		"fpassSpan.textContent = '—'",
		"gradeSpan.textContent = '—'",
		"const mk = agent.mistakes || {};",
		"const sr = agent.service_records || {};",
		"(agent.first_pass_rate * 100).toFixed(0)",
		"agent.first_pass_approved + ' of ' + agent.reviewed_steps",
		"String(agent.re_reviews)",
		"Re-reviews of steps this agent wrote (fix rounds when the verdict line records them)",
		"['high', mk.high ?? 0], ['med', mk.med ?? 0], ['low', mk.low ?? 0], ['unrated', mk.unrated ?? 0]",
		"mk.top.forEach(m => {",
		"link.href = '/missions#' + encodeURIComponent(m.mission_id)",
		"' — ' + m.mistake + ' (' + m.impact + ')'",
		"'total ' + (sr.total ?? 0) + ' (self-reported ' + (sr.self_reported ?? 0) + ', bishop-observed ' + (sr.bishop_observed ?? 0) + ')'",
		"gradeElem.textContent = agent.avg_grade",
		"' over ' + agent.graded_missions + ' graded mission'",
		"lastSpan.textContent = agent.last_active ? agent.last_active : '—'",
		"empty.textContent = 'No crew activity yet.'",
		"err.textContent = 'Cannot reach the service: ' + e.message",
		`<a href="/crew" class="on" aria-current="page">Crew</a>`,
		"card.tabIndex = 0",
		"card.role = 'button'",
		"card.setAttribute('aria-expanded'",
		"setAttribute('aria-label', 'Pagination')",
		"setAttribute('aria-current', 'page')",
	}
	for _, needle := range needles {
		t.Run(needle, func(t *testing.T) {
			if !strings.Contains(page, needle) {
				t.Fatalf("crew page does not contain %q", needle)
			}
		})
	}
	t.Run("no innerHTML in crew page", func(t *testing.T) {
		if !strings.Contains(serve(t, MissionsPageHandler()).Body.String(), "innerHTML") {
			t.Fatal("control failed: missions page no longer contains innerHTML, so the absence check proves nothing")
		}
		if strings.Contains(page, "innerHTML") {
			t.Fatal("crew page contains innerHTML; data must reach the DOM through textContent or text nodes only")
		}
	})
}

// TestMissionsPageFiltersAreLabelled checks that the search, harness, status,
// and grade filters in the missions page all have associated labels.
func TestMissionsPageFiltersAreLabelled(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	needles := []string{
		`<label for="q" style="display:contents">Search`,
		`<label for="harness" style="display:contents">Harness`,
		`<label for="status" style="display:contents">Status`,
		`<label for="grade" style="display:contents">Grade`,
	}
	for _, needle := range needles {
		t.Run(needle, func(t *testing.T) {
			if !strings.Contains(page, needle) {
				t.Fatalf("missions page does not contain labelled filter: %q", needle)
			}
		})
	}
}

// TestMissionsPageEscapesFindingStatus checks that the finding status tile
// escapes both the status key and its count value when rendering the findings
// tile subline, to prevent XSS via tainted API strings.
func TestMissionsPageEscapesFindingStatus(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	if !strings.Contains(page, "${esc(String(v))} ${esc(k)}") {
		t.Fatalf("missions page does not escape finding status counts and keys")
	}
}

// TestMissionsPageRendersReviewsSection is a source-text check only: it cannot
// run the page's JavaScript. It asserts that the mission page reads the HUD's
// reviews field with an empty-array fallback, renders a Reviews section, maps
// the approved and not-approved verdicts to their labels and badge classes
// with "unknown" and "defer" for any other value, passes every rendered field
// through esc(), renders a missing reviewed step or count as a dash rather
// than as an empty or zero value, and words the empty state.
func TestMissionsPageRendersReviewsSection(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	needles := []string{
		"d.reviews || []",
		"<h3>Reviews ",
		`{'approved': 'approve',`,
		`'not-approved': 'reject'}`,
		`vlabel = {'approved': 'approved',`,
		`'not-approved': 'not approved'}`,
		`esc(vlabel[r.verdict] || 'unknown')`,
		`esc(vclass[r.verdict] || 'defer')`,
		"esc(r.step)",
		"esc(r.reviews_step)",
		"esc(r.critical)",
		"esc(r.warnings)",
		"esc(r.fix_round)",
		"r.reviews_step == null ? '—'",
		"r.critical == null ? '—'",
		"r.warnings == null ? '—'",
		"r.fix_round == null ? '—'",
		`<div class="none">No review steps recorded.</div>`,
	}
	for _, needle := range needles {
		if !strings.Contains(page, needle) {
			t.Errorf("missions page does not contain %q", needle)
		}
	}
}
