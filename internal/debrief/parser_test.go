package debrief

import (
	"testing"
)

func TestParseMistakesImpactLowWithNote(t *testing.T) {
	// Verifies: impact level "low" with text after dash → impact="low", impactNote="..."
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Reports summarised instead of pasted | low — Bishop re-ran everything | Re-ran every mutant and gate | Keep summary in brief |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true, got ok=false")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "Bishop re-ran everything" {
		t.Errorf("expected impactNote='Bishop re-ran everything', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactMed(t *testing.T) {
	// Verifies: impact level "med" with text after dash
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Skipped the chmod | med — tests skip or fail | Fix round 15 | Pattern: set file modes |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "med" {
		t.Errorf("expected impact='med', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "tests skip or fail" {
		t.Errorf("expected impactNote='tests skip or fail', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactMediumNormalized(t *testing.T) {
	// Verifies: "medium" is normalized to "med"
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | medium — some note | Fix | Prevent |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "med" {
		t.Errorf("expected impact='med' (normalized from 'medium'), got %q", rows[0].Impact)
	}
}

func TestParseMistakesImpactHigh(t *testing.T) {
	// Verifies: impact level "high" with text after dash
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @bishop | Evidence cited wrong tests | high — two not-approved rounds | Rows rewritten | Citation-discipline |`
	rows, _, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "high" {
		t.Errorf("expected impact='high', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "two not-approved rounds" {
		t.Errorf("got impactNote=%q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactLowAlone(t *testing.T) {
	// Verifies: impact "low" with no dash or note text
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @lambert | Inserted entries mid-file | low | Fixed the same pass | Append-only check |`
	rows, _, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactEmDash(t *testing.T) {
	// Verifies: "—" (em dash) alone → impact="", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @lambert | Some issue here | — | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactEnDashAlone(t *testing.T) {
	// Verifies: "–" (en dash) alone → impact="", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @lambert | Some issue here | – | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactDoubleDash(t *testing.T) {
	// Verifies: "— —" (em dash, space, em dash) → impact="", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @vasquez | Deviation flagged | — — | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty note, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactNone(t *testing.T) {
	// Verifies: "none" (case-insensitive) in Impact cell → impact="", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @apone | Some review issue | none | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactUnknownWord(t *testing.T) {
	// Verifies: unknown first word before dash → impact="", note=entire cell
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Something | vague — unclear impact | Fix | Prevent |`
	rows, _, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "vague — unclear impact" {
		t.Errorf("expected whole cell as note, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactTrailingPunctuation(t *testing.T) {
	// Verifies: punctuation like '+' is stripped before matching impact level
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low+ | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low' after stripping punctuation, got %q", rows[0].Impact)
	}
}

func TestParseMistakesSeparatorStylePipes(t *testing.T) {
	// Verifies: separator row with |---|  style is properly skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue 1 | low | Fix 1 | Prev 1 |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Mistake != "Issue 1" {
		t.Errorf("got mistake=%q", rows[0].Mistake)
	}
}

func TestParseMistakesSeparatorStyleLongPipes(t *testing.T) {
	// Verifies: separator row with |-------|  style is properly skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|-------|-------|-------|-------|-------|
| @hicks | Issue 1 | low | Fix 1 | Prev 1 |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestParseMistakesNoneMaterialRow(t *testing.T) {
	// Verifies: "None material" in Agent cell (starts with "None" + space) → row stores nothing and isn't counted as skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| None material; every review traced data flow | Some issue | — | — | Paste diffs |
| @hicks | Real issue | low | Fix | Prevent |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None material row excluded), got %d", len(rows))
	}
	if rows[0].Mistake != "Real issue" {
		t.Errorf("expected Real issue, got %q", rows[0].Mistake)
	}
}

func TestParseMistakesEscapedPipe(t *testing.T) {
	// Verifies: escaped pipe \| in a cell is unescaped to |
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Mistake \| with escaped pipe | low | Fix | Prevent |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Mistake != "Mistake | with escaped pipe" {
		t.Errorf("expected unescaped pipe, got %q", rows[0].Mistake)
	}
}

func TestParseMistakesWrongCellCount(t *testing.T) {
	// Verifies: row with wrong cell count is skipped and counted; other rows kept
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Too | few | cells |
| @lambert | Correct count row | med | Action | Prevent |
| @hicks | Also | too | many | cells | extra | extra |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 2 {
		t.Errorf("expected skipped=2, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 kept row, got %d", len(rows))
	}
	if rows[0].Mistake != "Correct count row" {
		t.Errorf("got kept row: %q", rows[0].Mistake)
	}
}

func TestParseMistakesTrailingProse(t *testing.T) {
	// Verifies: trailing prose after table ends is ignored
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Row 1 | low | Fix 1 | Prev 1 |
| @lambert | Row 2 | med | Fix 2 | Prev 2 |

This is trailing prose that should not be parsed as a row.
More prose here that spans multiple lines.`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Mistake != "Row 1" || rows[1].Mistake != "Row 2" {
		t.Errorf("expected Row 1 and Row 2")
	}
}

func TestParseMistakesEmptyTable(t *testing.T) {
	// Verifies: empty table (header and separator only) → ok=true, 0 rows
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for empty table, got %d", len(rows))
	}
}

func TestParseMistakesRealDataFixture(t *testing.T) {
	// Verifies: real data from kirsch mission-20261002-01 DEBRIEF.md
	// Sub-Agent Mistakes and Corrections table, first 4 rows
	section := `| Agent | Mistake | Impact | Corrective Action | Prevention Next Time |
|---|---|---|---|---|
| @hicks | Reports summarised instead of pasted (steps 1, 3, 11, 15, 19) | low — Bishop re-ran everything | Re-ran every mutant and gate | Keep "a summary counts as not done" in every brief |
| @hicks | Skipped the required os.Chmod in the mode tests (step 5) | med — tests skip or fail under a restrictive umask | Fix round step 15 | Pattern: set file modes with Chmod |
| @hicks | Body-withdrawn test logged instead of failing when no size was found, and used a loose heuristic (step 11) | med — could pass vacuously | Step 13 | Precondition searches must fail, not log |
| @lambert | Inserted two PATTERNS entries mid-file during the learning pass | low — fixed the same pass | Moved them to the end | Append-only check in the verification |`
	rows, skipped, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}

	tests := []struct {
		idx  int
		name string
		exp  Mistake
	}{
		{
			idx:  0,
			name: "row 1: hicks summarised",
			exp: Mistake{
				AgentRaw:   "@hicks",
				Mistake:    "Reports summarised instead of pasted (steps 1, 3, 11, 15, 19)",
				Impact:     "low",
				ImpactNote: "Bishop re-ran everything",
				Correction: "Re-ran every mutant and gate",
				Prevention: "Keep \"a summary counts as not done\" in every brief",
			},
		},
		{
			idx:  1,
			name: "row 2: hicks chmod",
			exp: Mistake{
				AgentRaw:   "@hicks",
				Mistake:    "Skipped the required os.Chmod in the mode tests (step 5)",
				Impact:     "med",
				ImpactNote: "tests skip or fail under a restrictive umask",
				Correction: "Fix round step 15",
				Prevention: "Pattern: set file modes with Chmod",
			},
		},
		{
			idx:  2,
			name: "row 3: hicks body-withdrawn",
			exp: Mistake{
				AgentRaw:   "@hicks",
				Mistake:    "Body-withdrawn test logged instead of failing when no size was found, and used a loose heuristic (step 11)",
				Impact:     "med",
				ImpactNote: "could pass vacuously",
				Correction: "Step 13",
				Prevention: "Precondition searches must fail, not log",
			},
		},
		{
			idx:  3,
			name: "row 4: lambert patterns",
			exp: Mistake{
				AgentRaw:   "@lambert",
				Mistake:    "Inserted two PATTERNS entries mid-file during the learning pass",
				Impact:     "low",
				ImpactNote: "fixed the same pass",
				Correction: "Moved them to the end",
				Prevention: "Append-only check in the verification",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rows[tt.idx].AgentRaw != tt.exp.AgentRaw {
				t.Errorf("agentRaw: got %q, want %q", rows[tt.idx].AgentRaw, tt.exp.AgentRaw)
			}
			if rows[tt.idx].Impact != tt.exp.Impact {
				t.Errorf("impact: got %q, want %q", rows[tt.idx].Impact, tt.exp.Impact)
			}
			if rows[tt.idx].ImpactNote != tt.exp.ImpactNote {
				t.Errorf("impactNote: got %q, want %q", rows[tt.idx].ImpactNote, tt.exp.ImpactNote)
			}
			if rows[tt.idx].Mistake != tt.exp.Mistake {
				t.Errorf("mistake: got %q, want %q", rows[tt.idx].Mistake, tt.exp.Mistake)
			}
			if rows[tt.idx].Correction != tt.exp.Correction {
				t.Errorf("correction: got %q, want %q", rows[tt.idx].Correction, tt.exp.Correction)
			}
			if rows[tt.idx].Prevention != tt.exp.Prevention {
				t.Errorf("prevention: got %q, want %q", rows[tt.idx].Prevention, tt.exp.Prevention)
			}
		})
	}
}

func TestParseMistakesEmptySection(t *testing.T) {
	// Verifies: empty section → ok=false, 0 rows
	rows, _, ok := ParseMistakes("")

	if ok {
		t.Errorf("expected ok=false for empty section, got ok=true")
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestParseMistakesAgentCaseInsensitive(t *testing.T) {
	// Verifies: "AGENT" (case-insensitive) in header is recognized
	section := `| AGENT | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)

	if !ok {
		t.Fatal("expected ok=true for case-insensitive AGENT header")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestParseMistakesImpactEnDash(t *testing.T) {
	// Verifies: en dash (–) is recognized as separator
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low – with note | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "with note" {
		t.Errorf("expected impactNote='with note', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactSpacedHyphen(t *testing.T) {
	// Verifies: spaced hyphen " - " is recognized as separator
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | med - important note | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "med" {
		t.Errorf("expected impact='med', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "important note" {
		t.Errorf("expected impactNote='important note', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactLowRiskNotSplit(t *testing.T) {
	// Verifies: "low-risk" (bare hyphen, no spaces) is NOT split; treated as unknown word
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low-risk | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact for 'low-risk', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "low-risk" {
		t.Errorf("expected impactNote='low-risk', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactBareDashAlone(t *testing.T) {
	// Verifies: bare "-" alone is treated as empty (all dashes)
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | - | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact for '-', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote for '-', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactEmptyCell(t *testing.T) {
	// Verifies: empty impact cell → impact="", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactBareMedium(t *testing.T) {
	// Verifies: bare "medium" with no dash → impact="med", note=""
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | medium | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "med" {
		t.Errorf("expected impact='med', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty impactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactNoDashUnknownWord(t *testing.T) {
	// Verifies: no-dash unknown word "TBD" → impact="", note="TBD"
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | TBD | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "" {
		t.Errorf("expected empty impact, got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "TBD" {
		t.Errorf("expected impactNote='TBD', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactLowImpact(t *testing.T) {
	// Verifies: "low impact" (no dash) → impact="low", note="impact"
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low impact | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "impact" {
		t.Errorf("expected impactNote='impact', got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesImpactLowMinorReran(t *testing.T) {
	// Verifies: "low (minor) — reran" with parenthetical → impact="low", note="(minor) reran"
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low (minor) — reran | Action | Prevention |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Impact != "low" {
		t.Errorf("expected impact='low', got %q", rows[0].Impact)
	}
	if rows[0].ImpactNote != "(minor) reran" {
		t.Errorf("expected impactNote='(minor) reran', got %q", rows[0].ImpactNote)
	}
}

// Separator style tests
func TestParseMistakesSeparatorAlignedStyle(t *testing.T) {
	// Verifies: separator row with "| --- |" (aligned spaced) style is skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
| --- | --- | --- | --- | --- |
| @hicks | Issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
}

func TestParseMistakesSeparatorColonAlignStyle(t *testing.T) {
	// Verifies: separator row with "|:---|" (colon-aligned) style is skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|:---|:---|:---|:---|:---|
| @hicks | Issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
}

// Empty cell tests
func TestParseMistakesEmptyCorrectionRow(t *testing.T) {
	// Verifies: row with empty Correction and Prevention cells is kept (5 cells total)
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Issue | low | | |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if rows[0].Correction != "" {
		t.Errorf("expected empty correction, got %q", rows[0].Correction)
	}
	if rows[0].Prevention != "" {
		t.Errorf("expected empty prevention, got %q", rows[0].Prevention)
	}
}

func TestParseMistakesEmptyAgentCell(t *testing.T) {
	// Verifies: row with empty Agent cell is kept (5 cells total)
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| | Issue here | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if rows[0].AgentRaw != "" {
		t.Errorf("expected empty agentRaw, got %q", rows[0].AgentRaw)
	}
}

// None row variants
func TestParseMistakesNoneRowFiveCells(t *testing.T) {
	// Verifies: "None on this mission" as first cell (5 cells total) is not counted as skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| None on this mission | — | — | — | — |
| @hicks | Real issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None row excluded), got %d", len(rows))
	}
}

func TestParseMistakesNoneRowAgentNoneBare(t *testing.T) {
	// Verifies: Agent cell "None" (bare) is a None row
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| None | — | — | — | — |
| @hicks | Real issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0 (None rows not counted), got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None row excluded), got %d", len(rows))
	}
}

func TestParseMistakesNoneRowAgentCaseInsensitive(t *testing.T) {
	// Verifies: Agent cell "NONE" (uppercase) is a None row
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| NONE | — | — | — | — |
| @hicks | Real issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0 (None rows not counted), got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None row excluded), got %d", len(rows))
	}
}

func TestParseMistakesNoneRowAgentWithPunctuation(t *testing.T) {
	// Verifies: Agent cell "None." (with punctuation) is a None row
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| None. | — | — | — | — |
| @hicks | Real issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0 (None rows not counted), got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None row excluded), got %d", len(rows))
	}
}

func TestParseMistakesNoneRowOneCell(t *testing.T) {
	// Verifies: "| None on this mission |" (1 cell) is a None row, not counted as skipped
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| None on this mission |
| @hicks | Real issue | low | Action | Prevention |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0 (None rows not counted), got %d", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (None row excluded), got %d", len(rows))
	}
}

func TestParseMistakesNoneWordBoundary(t *testing.T) {
	// Verifies: "Nonexistent" (starts with "none" but no word boundary) is NOT a None word
	// This tests the word boundary check by placing it in the Agent cell
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| Nonexistent agent | Issue here | low | Fix | Prevent |`
	rows, _, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if rows[0].AgentRaw != "Nonexistent agent" {
		t.Errorf("expected 'Nonexistent agent', got %q", rows[0].AgentRaw)
	}
}

// Trailing prose tests
func TestParseMistakesTrailingProseNoBlankLine(t *testing.T) {
	// Verifies: prose with no blank line (no pipe) ends the table; trailing prose not included
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Row 1 | low | Fix | Prevent |
Some prose here with no blank line and no pipe.`
	rows, skipped, ok := ParseMistakes(section)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected ok=true, 1 row, got %d rows", len(rows))
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0, got %d", skipped)
	}
	if rows[0].ImpactNote != "" {
		t.Errorf("expected empty ImpactNote, got %q", rows[0].ImpactNote)
	}
}

func TestParseMistakesTrailingProseWithPipe(t *testing.T) {
	// Verifies: a prose line that contains a pipe does NOT end the table. The parser sees
	// a pipe and treats the line as a row; it splits into 2 cells, so it is skipped and
	// counted, and reading continues: the valid row after it is still stored.
	// Entry point: ParseMistakes.
	section := `| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @hicks | Row 1 | low | Fix | Prevent |
This prose contains a pipe | but not a full row
| @lambert | Row 2 | med | Fix | Prevent |`
	rows, skipped, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("pipe-prose: expected ok=true")
	}
	if skipped != 1 {
		t.Errorf("pipe-prose: expected skipped=1 (the 2-cell prose line), got %d", skipped)
	}
	if len(rows) != 2 {
		t.Fatalf("pipe-prose: expected 2 rows (valid row after the prose line is still read), got %d", len(rows))
	}
	if rows[0].Mistake != "Row 1" {
		t.Errorf("pipe-prose: row before the prose line: got mistake %q, want %q", rows[0].Mistake, "Row 1")
	}
	if rows[1].Mistake != "Row 2" {
		t.Errorf("pipe-prose: row after the prose line: got mistake %q, want %q", rows[1].Mistake, "Row 2")
	}
}

// NoAgentTable with positive control
func TestParseMistakesNoAgentTableWithControl(t *testing.T) {
	// Verifies: absence of Agent header → ok=false (negative control)
	section := `| Person | Problem | Status | Action | Lesson |
|---|---|---|---|---|
| @hicks | Issue | low | Fixed | Learn |`
	rows, _, ok := ParseMistakes(section)
	if ok {
		t.Errorf("expected ok=false for missing Agent header, got ok=true")
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestParseMistakesAgentTablePositiveControl(t *testing.T) {
	// Verifies: presence of Agent header → ok=true (positive control for absence test)
	section := `| Person | Problem | Status | Action | Lesson |
|---|---|---|---|---|
| @hicks | Issue | low | Fixed | Learn |
|
| Agent | Mistake | Impact | Fix | Prevent |
|---|---|---|---|---|
| @lambert | Real issue | med | Action | Prevent |`
	rows, _, ok := ParseMistakes(section)
	if !ok {
		t.Fatal("expected ok=true when Agent table is present")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

// mistakesTestHeader is the header and separator rows every table below starts with.
const mistakesTestHeader = "| Agent | Mistake | Impact | Fix | Prevent |\n|---|---|---|---|---|\n"

// TestParseMistakesImpactCellForms drives the Impact cell rules end to end through
// ParseMistakes (the entry point; parseImpact is reached only from there). Each subtest
// puts one cell form in the Impact cell of a 5-cell row and names the cell in its
// failure text. The pairs cover: a note after "none" is kept (mirror: the same note
// after "low"), each trailing character stripped from the first word (mirror: the
// same word with no trailing character), and case-insensitivity.
func TestParseMistakesImpactCellForms(t *testing.T) {
	cases := []struct {
		name       string
		cell       string
		wantImpact string
		wantNote   string
	}{
		{"none_dash_note", "none — see note", "", "see note"},
		{"low_dash_note", "low — see note", "low", "see note"},
		{"none_en_dash_note", "none – see note", "", "see note"},
		{"none_spaced_hyphen_note", "none - see note", "", "see note"},
		{"bare_none", "none", "", ""},
		{"bare_NONE_upper", "NONE", "", ""},
		{"LOW_upper_dash_note", "LOW — x", "low", "x"},
		{"none_colon_note", "none: x", "", "x"},
		{"none_comma_note", "none, x", "", "x"},
		{"none_semicolon_note", "none; x", "", "x"},
		{"none_period_note", "none. x", "", "x"},
		{"none_rest_of_head_and_tail", "none really — see note", "", "really see note"},
		{"none_rest_of_head_only", "none needed", "", "needed"},
		{"nonexistent_word_unrecognised", "nonexistent — see note", "", "nonexistent — see note"},
		{"low_colon_note", "low: x", "low", "x"},
		{"low_semicolon_note", "low; x", "low", "x"},
		{"low_comma_note", "low, x", "low", "x"},
		{"low_period_note", "low. x", "low", "x"},
		{"low_plus_note", "low+ x", "low", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			section := mistakesTestHeader + "| @hicks | Issue | " + tc.cell + " | Fix | Prevent |"
			rows, skipped, ok := ParseMistakes(section)
			if !ok || skipped != 0 || len(rows) != 1 {
				t.Fatalf("cell %q: ok=%v skipped=%d rows=%d, want ok=true skipped=0 rows=1", tc.cell, ok, skipped, len(rows))
			}
			if rows[0].Impact != tc.wantImpact {
				t.Errorf("cell %q: impact = %q, want %q", tc.cell, rows[0].Impact, tc.wantImpact)
			}
			if rows[0].ImpactNote != tc.wantNote {
				t.Errorf("cell %q: impact note = %q, want %q", tc.cell, rows[0].ImpactNote, tc.wantNote)
			}
		})
	}
}

// TestParseMistakesNoneRuleMistakeCell drives None-row rule (b) through ParseMistakes
// (the entry point). Rule (b) looks at the Mistake cell (starts with a None word) and
// the Impact cell (empty, "none", "—", "–" or only hyphens). Every subtest puts one
// candidate row before a control row that must always be stored, so an absent
// candidate is told apart from a table that produced nothing at all. The candidates
// that must be kept are the mirrors of the ones that must be dropped.
func TestParseMistakesNoneRuleMistakeCell(t *testing.T) {
	const control = "| @lambert | Control | med | Fix | Prevent |"
	cases := []struct {
		name        string
		row         string
		wantStored  bool
		wantSkipped int
	}{
		{"impact_low_stored", "| @hicks | None of the tests ran | low | Fix | Prevent |", true, 0},
		{"impact_em_dash_dropped", "| @hicks | None of the tests ran | — | Fix | Prevent |", false, 0},
		{"impact_en_dash_dropped", "| @hicks | None of the tests ran | – | Fix | Prevent |", false, 0},
		{"impact_empty_dropped", "| @hicks | None of the tests ran | | Fix | Prevent |", false, 0},
		{"impact_hyphen_dropped", "| @hicks | None of the tests ran | - | Fix | Prevent |", false, 0},
		{"impact_hyphens_dropped", "| @hicks | None of the tests ran | --- | Fix | Prevent |", false, 0},
		{"impact_none_dropped", "| @hicks | None of the tests ran | none | Fix | Prevent |", false, 0},
		{"impact_NONE_upper_dropped", "| @hicks | None of the tests ran | NONE | Fix | Prevent |", false, 0},
		{"impact_none_with_note_stored", "| @hicks | None of the tests ran | none — see note | Fix | Prevent |", true, 0},
		{"impact_nonesuch_stored", "| @hicks | None of the tests ran | nonesuch | Fix | Prevent |", true, 0},
		{"mistake_nonexistent_stored", "| @hicks | Nonexistent tests ran | — | Fix | Prevent |", true, 0},
		{"four_cells_none_dropped_not_skipped", "| @hicks | None of the tests ran | — | Fix |", false, 0},
		{"four_cells_real_mistake_skipped", "| @hicks | Some mistake | — | Fix |", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, skipped, ok := ParseMistakes(mistakesTestHeader + tc.row + "\n" + control)
			if !ok {
				t.Fatalf("row %q: expected ok=true", tc.row)
			}
			if skipped != tc.wantSkipped {
				t.Errorf("row %q: skipped = %d, want %d", tc.row, skipped, tc.wantSkipped)
			}
			wantRows := 1
			if tc.wantStored {
				wantRows = 2
			}
			if len(rows) != wantRows {
				t.Fatalf("row %q: %d rows stored, want %d (stored=%v)", tc.row, len(rows), wantRows, tc.wantStored)
			}
			if rows[len(rows)-1].Mistake != "Control" {
				t.Errorf("row %q: control row not last, got mistake %q", tc.row, rows[len(rows)-1].Mistake)
			}
			if tc.wantStored && rows[0].AgentRaw != "@hicks" {
				t.Errorf("row %q: stored candidate has agent_raw %q, want @hicks", tc.row, rows[0].AgentRaw)
			}
		})
	}
}
