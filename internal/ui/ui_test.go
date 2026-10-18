package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
//
// The name says "Renders" but the entry point is CrewPageHandler's served HTML
// text, searched with strings.Contains; nothing is rendered. The last needle,
// `if (!ui || !view.contains(ui.q)) ui = mountCrew();`, pins the one line in
// the page's render function that mounts the filter once and reuses it. It
// would still pass if paintCrew cleared #view or rebuilt #cq, so it does not
// show that the filter keeps focus.
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
		"if (!ui || !view.contains(ui.q)) ui = mountCrew();",
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
		`<label for="q" class="vh">Search</label><input id="q"`,
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

// TestMissionsPageFindingStatusSourceText is a source-text check only: it
// cannot run the page's JavaScript. It asserts that the mission page template
// escapes both the status key and its count value in the findings tile subline
// by checking the template string in the page source.
func TestMissionsPageFindingStatusSourceText(t *testing.T) {
	page := serve(t, MissionsPageHandler()).Body.String()
	if !strings.Contains(page, "${esc(String(v))} ${esc(k)}") {
		t.Fatalf("missions page does not escape finding status counts and keys")
	}
	// Absence control: the raw form without esc() must NOT appear.
	if strings.Contains(page, "${v} ${k}") {
		t.Fatalf("missions page contains unescaped finding status template")
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

// TestPagerConstDeclaredOnce checks that each page using pagerHTML template
// (missions, triage, performance) declares it exactly once. A duplicate
// declaration is a SyntaxError that prevents the page from loading.
func TestPagerConstDeclaredOnce(t *testing.T) {
	pages := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{"missions", MissionsPageHandler()},
		{"triage", TriagePageHandler()},
		{"performance", PerformancePageHandler()},
	}
	for _, p := range pages {
		page := serve(t, p.handler).Body.String()
		count := strings.Count(page, "const pagerHTML")
		if count != 1 {
			t.Errorf("%s page declares pagerHTML %d times, want 1", p.name, count)
		}
	}
}

// TestSharedShellIdentical checks that the CSS shared shell block
// (identical across all four pages) is byte-for-byte identical in each page.
func TestSharedShellIdentical(t *testing.T) {
	// Pages in sorted-name order; the first (crew) is the reference every
	// other page is compared against.
	pages := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{"crew", CrewPageHandler()},
		{"missions", MissionsPageHandler()},
		{"performance", PerformancePageHandler()},
		{"triage", TriagePageHandler()},
	}

	const sharedStartMarker = "/* ---- Shared shell (same block on every page): header, in-page tabs, pager ---- */"
	// Shared shell ends just after @media (max-width: 860px) { .subbar }
	const sharedEndMarker = "@media (max-width: 860px) { .subbar { position:static; padding:0 16px; flex-wrap:wrap } }"

	shells := make([]string, len(pages))
	for i, p := range pages {
		page := serve(t, p.handler).Body.String()
		start := strings.Index(page, sharedStartMarker)
		if start == -1 {
			t.Fatalf("%s page missing shared shell start marker", p.name)
		}
		end := strings.Index(page[start:], sharedEndMarker)
		if end == -1 {
			t.Fatalf("%s page missing shared shell end marker", p.name)
		}
		end += start + len(sharedEndMarker)
		shells[i] = page[start:end]
	}

	ref := shells[0]
	for i := 1; i < len(pages); i++ {
		if shells[i] == ref {
			continue
		}
		n := 0
		for n < len(ref) && n < len(shells[i]) && ref[n] == shells[i][n] {
			n++
		}
		t.Errorf("%s page shared shell differs from %s page at byte %d (lengths %d vs %d)",
			pages[i].name, pages[0].name, n, len(shells[i]), len(ref))
	}
}

// TestPerformanceModalKeyListenerSourceText is a source-text check only: it
// cannot run the page's JavaScript. It asserts that the performance page source
// contains the string adding the modalKeys keydown listener exactly once, the
// string removing it exactly once, and no mention of restoreFocus. It does not
// show that the add and remove sit in openModal and closeModal, that they are
// wired to the buttons, or that Escape and the Tab trap work. The restoreFocus
// absence check is paired with a presence check through the same strings.Contains
// path on a comparable identifier, focusSel, which the page does contain.
func TestPerformanceModalKeyListenerSourceText(t *testing.T) {
	page := serve(t, PerformancePageHandler()).Body.String()
	add := "document.addEventListener('keydown', modalKeys)"
	rm := "document.removeEventListener('keydown', modalKeys)"
	if n := strings.Count(page, add); n != 1 {
		t.Errorf("performance page adds the modalKeys listener %d times, want 1", n)
	}
	if n := strings.Count(page, rm); n != 1 {
		t.Errorf("performance page removes the modalKeys listener %d times, want 1", n)
	}
	if !strings.Contains(page, "focusSel") {
		t.Error("control failed: performance page no longer contains focusSel, so the restoreFocus absence check proves nothing")
	}
	if strings.Contains(page, "restoreFocus") {
		t.Error("performance page still mentions restoreFocus")
	}
}

// TestTriageModelTruncatesBeforeEscapeSourceText is a source-text check only:
// it cannot run the page's JavaScript. It asserts that the triage page source
// contains the code-point truncation `[...String(r.model)].slice(0, 27)`
// and does not contain `esc(r.model).substring`, the earlier form that cut the
// string after escaping and could split an entity. The absence check is paired
// with the presence check on the same served text.
func TestTriageModelTruncatesBeforeEscapeSourceText(t *testing.T) {
	page := serve(t, TriagePageHandler()).Body.String()
	if !strings.Contains(page, "[...String(r.model)].slice(0, 27).join('')") {
		t.Error("triage page does not truncate r.model by code point before escaping")
	}
	if strings.Contains(page, "esc(r.model).substring") {
		t.Error("triage page truncates r.model after escaping (esc(r.model).substring)")
	}
}

// scriptBlock matches an inline script element with or without attributes. It
// ends at the first closing script tag, so a literal one inside a JavaScript
// string would cut a block short.
var scriptBlock = regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`)

// nodeCheck writes src to a temp .js file and runs `node --check` on it,
// returning node's combined output and the command's error (nil when node
// accepted the syntax).
func nodeCheck(t *testing.T, node, src string) (string, error) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "block.js")
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", f).CombinedOutput()
	return string(out), err
}

// TestEmbeddedScriptsParse extracts every inline script block (scriptBlock) from
// each served page and runs `node --check` on it. It checks syntax only: it does
// not execute the scripts, so it says nothing about runtime behaviour. It skips
// when node is not on PATH. The control subtest feeds node a duplicate const
// declaration and requires a failure, so a pass cannot come from a node that
// accepts everything.
func TestEmbeddedScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; inline scripts were not syntax-checked")
	}
	t.Run("control rejects duplicate const", func(t *testing.T) {
		if out, err := nodeCheck(t, node, "const a = 1;\nconst a = 2;\n"); err == nil {
			t.Fatalf("control failed: node --check accepted a duplicate const (output %q)", out)
		}
	})
	pages := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{"crew", CrewPageHandler()},
		{"missions", MissionsPageHandler()},
		{"performance", PerformancePageHandler()},
		{"triage", TriagePageHandler()},
	}
	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			blocks := scriptBlock.FindAllStringSubmatch(serve(t, p.handler).Body.String(), -1)
			if len(blocks) == 0 {
				t.Fatalf("%s page: no <script> block found, nothing was checked", p.name)
			}
			for i, b := range blocks {
				if out, err := nodeCheck(t, node, b[1]); err != nil {
					t.Errorf("%s page: script block %d does not parse: %v\n%s", p.name, i+1, err, out)
				}
			}
		})
	}
}
