package review

import (
	"fmt"
	"strings"
	"testing"
)

func ip(n int) *int { return &n }

// show formats a Verdict on one line, with nil counts as "-".
func show(v Verdict) string {
	f := func(p *int) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprint(*p)
	}
	return fmt.Sprintf("verdict=%q critical=%s warnings=%s reviews_step=%q fix_round=%s source=%s",
		v.Verdict, f(v.Critical), f(v.Warnings), v.ReviewsStep, f(v.FixRound), v.Source)
}

// TestParse runs Parse on fixed verdict lines and on free-text wordings
// modelled on live reviewer notes, one subtest per case, comparing every
// field.
func TestParse(t *testing.T) {
	cases := []struct {
		name, notes, summary string
		want                 Verdict
	}{
		// Fixed verdict lines.
		{"line in notes beats summary text", "Verdict: APPROVED; critical=0; warnings=0; reviews_step=2; fix_round=0", "NOT APPROVED, 3 CRITICAL", Verdict{Approved, ip(0), ip(0), "2", ip(0), SourceLine}},
		{"line not approved without fix round", "Verdict: NOT APPROVED; critical=1; warnings=4; reviews_step=3", "", Verdict{NotApproved, ip(1), ip(4), "3", nil, SourceLine}},
		{"line in summary beats notes text", "APPROVED, no CRITICAL", "Verdict: NOT APPROVED; critical=2; warnings=0; reviews_step=7", Verdict{NotApproved, ip(2), ip(0), "7", nil, SourceLine}},
		{"line with fix round", "Review of 4. Verdict: APPROVED; critical=0; warnings=2; reviews_step=4a; fix_round=1", "", Verdict{Approved, ip(0), ip(2), "4a", ip(1), SourceLine}},
		{"line not approved with double space", "Verdict: NOT  APPROVED; critical=1; warnings=0; reviews_step=5", "", Verdict{NotApproved, ip(1), ip(0), "5", nil, SourceLine}},
		{"lowercase verdict line is not read", "verdict: approved; critical=0; warnings=0; reviews_step=1", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"line hyphenated not approved", "Verdict: NOT-APPROVED; critical=0; warnings=0; reviews_step=4", "", Verdict{NotApproved, ip(0), ip(0), "4", nil, SourceLine}},
		{"line in both fields notes wins", "Verdict: APPROVED; critical=0; warnings=0; reviews_step=1", "Verdict: NOT APPROVED; critical=1; warnings=0; reviews_step=1", Verdict{Approved, ip(0), ip(0), "1", nil, SourceLine}},
		// Free-text verdict wording.
		{"text approved with digit counts", "APPROVED, 0 CRITICAL, 1 WARNING, 1 SUGGESTION", "", Verdict{Approved, ip(0), ip(1), "", nil, SourceText}},
		{"text not approved with digit count", "Review of 8a: NOT APPROVED — 2 CRITICAL (C1 mutant missing)", "", Verdict{NotApproved, ip(2), nil, "8a", nil, SourceText}},
		{"text hyphenated not approved", "NOT-APPROVED — 1 CRITICAL", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		{"title case not approved", "Not approved — 1 CRITICAL, commit after discard succeeds", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		{"mixed case not approved", "Not Approved — 1 CRITICAL", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		{"lowercase negation", "not approved yet", "", Verdict{NotApproved, nil, nil, "", nil, SourceText}},
		{"text double space not approved", "NOT  APPROVED — 1 CRITICAL", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		{"title case hyphen not approved", "Not-approved — 2 CRITICAL", "", Verdict{NotApproved, ip(2), nil, "", nil, SourceText}},
		{"title case approved", "Approved, no CRITICAL; 3 WARNINGS (README overclaim)", "", Verdict{Approved, ip(0), ip(3), "", nil, SourceText}},
		{"title case approved by", "Approved by the operator", "", Verdict{Approved, nil, nil, "", nil, SourceText}},
		{"text no critical", "Reviewed the four subagent files — APPROVED, no CRITICAL, 5 suggestions", "", Verdict{Approved, ip(0), nil, "", nil, SourceText}},
		{"text zero critical", "APPROVE, zero CRITICAL", "", Verdict{Approved, ip(0), nil, "", nil, SourceText}},
		{"text number word critical", "NOT APPROVED, one CRITICAL, patches only", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		{"text three critical ten warnings", "NOT APPROVED, three CRITICAL, ten warnings", "", Verdict{NotApproved, ip(3), ip(10), "", nil, SourceText}},
		{"changes requested is not approved", "Re-review returned CHANGES REQUESTED — six CRITICAL findings, one warning", "", Verdict{NotApproved, ip(6), ip(1), "", nil, SourceText}},
		{"changes required is not approved", "Review of chunk 9B: CHANGES REQUIRED — logic", "", Verdict{NotApproved, nil, nil, "", nil, SourceText}},
		{"not clean is not approved", "NOT CLEAN — 1 critical (docblock false), 2 warnings", "", Verdict{NotApproved, ip(1), ip(2), "", nil, SourceText}},
		{"no-go is not approved", "NO-GO: staging counts off", "", Verdict{NotApproved, nil, nil, "", nil, SourceText}},
		{"approve without d", "APPROVE — M1 closed; non-logic debt only", "", Verdict{Approved, nil, nil, "", nil, SourceText}},
		{"leftmost verdict word wins", "Round 1 NOT APPROVED, now APPROVED", "", Verdict{NotApproved, nil, nil, "", nil, SourceText}},
		{"step number is not a count", "step 23 critical path; NOT APPROVED, 1 CRITICAL", "", Verdict{NotApproved, ip(1), nil, "", nil, SourceText}},
		// Notes and summary.
		{"verdict from summary step from notes", "Review of step 23", "Escape-parser cluster reviewed — APPROVED, GO, no CRITICAL.", Verdict{Approved, ip(0), nil, "23", nil, SourceText}},
		{"notes verdict beats summary verdict", "APPROVED, no CRITICAL", "NOT APPROVED", Verdict{Approved, ip(0), nil, "", nil, SourceText}},
		{"counts not merged from notes", "2 CRITICAL, 3 warnings found", "NOT APPROVED", Verdict{NotApproved, nil, nil, "", nil, SourceText}},
		{"step from summary", "APPROVED", "Review of step 9", Verdict{Approved, nil, nil, "9", nil, SourceText}},
		{"reviewed step names the step", "Reviewed step 6 — APPROVED", "", Verdict{Approved, nil, nil, "6", nil, SourceText}},
		{"review of n with period names the step", "Review of 12. CHANGES REQUIRED", "", Verdict{NotApproved, nil, nil, "12", nil, SourceText}},
		{"review of n files names no step", "APPROVED; review of 2 files", "", Verdict{Approved, nil, nil, "", nil, SourceText}},
		{"review of n with comma names the step", "Review of 5, APPROVED", "", Verdict{Approved, nil, nil, "5", nil, SourceText}},
		{"review of n with semicolon names the step", "Review of 6; APPROVED", "", Verdict{Approved, nil, nil, "6", nil, SourceText}},
		{"review of n with paren names the step", "APPROVED (review of 7)", "", Verdict{Approved, nil, nil, "7", nil, SourceText}},
		{"review of n at end names the step", "APPROVED after review of 9", "", Verdict{Approved, nil, nil, "9", nil, SourceText}},
		// No verdict wording. The uppercase control reads the same sentence as a
		// verdict through the same path.
		{"lowercase approved is not a verdict", "approved by the operator after review", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"uppercase approved control", "APPROVED by the operator after review", "", Verdict{Approved, nil, nil, "", nil, SourceText}},
		{"approved inside a word is not a verdict", "UNAPPROVED draft", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"no-go inside a word is not a verdict", "NO-GOAL set", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"go alone is not a verdict", "GO — staging fine", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"major is not a verdict", "MAJOR: 1 open", "", Verdict{"", nil, nil, "", nil, SourceNone}},
		{"no verdict wording", "Review step 3 against code", "", Verdict{"", nil, nil, "3", nil, SourceNone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, want := show(Parse(c.notes, c.summary)), show(c.want); got != want {
				t.Errorf("Parse(%q, %q) = %s, want %s", c.notes, c.summary, got, want)
			}
		})
	}
}

// TestNumberWordsMatchCountWords checks that every number word countWords
// matches has a value in numberWords, and that numberWords has no word that
// countWords does not match.
func TestNumberWordsMatchCountWords(t *testing.T) {
	words := map[string]bool{}
	for _, w := range strings.Split(countWords, "|") {
		if w == `\d+` {
			continue
		}
		words[w] = true
		if _, ok := numberWords[w]; !ok {
			t.Errorf("countWords has %q but numberWords has no value for it", w)
		}
	}
	for w := range numberWords {
		if !words[w] {
			t.Errorf("numberWords has %q but countWords does not match it", w)
		}
	}
}
