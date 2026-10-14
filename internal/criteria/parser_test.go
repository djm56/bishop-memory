package criteria

import (
	"testing"
)

func TestParseBriefCriteria(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		want     []Criterion
		wantZero bool // true if we expect empty/nil result
	}{
		{
			name:     "empty section",
			section:  "",
			wantZero: true,
		},
		{
			name:     "single criterion",
			section:  "- First criterion",
			want:     []Criterion{{Text: "- First criterion", Result: "", Evidence: ""}},
			wantZero: false,
		},
		{
			name: "multiple criteria",
			section: `- First criterion
- Second criterion
- Third criterion`,
			want: []Criterion{
				{Text: "- First criterion", Result: "", Evidence: ""},
				{Text: "- Second criterion", Result: "", Evidence: ""},
				{Text: "- Third criterion", Result: "", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "criteria with star bullets",
			section: `* First criterion
* Second criterion`,
			want: []Criterion{
				{Text: "* First criterion", Result: "", Evidence: ""},
				{Text: "* Second criterion", Result: "", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name:     "section with no bullets",
			section:  "Just some text without bullets",
			wantZero: true,
		},
		{
			name: "section with leading spaces",
			section: `  - Indented criterion
  - Another indented`,
			want: []Criterion{
				{Text: "- Indented criterion", Result: "", Evidence: ""},
				{Text: "- Another indented", Result: "", Evidence: ""},
			},
			wantZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBriefCriteria(tt.section)
			if tt.wantZero {
				if len(got) != 0 {
					t.Errorf("want empty result, got %d criteria", len(got))
				}
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("got %d criteria, want %d", len(got), len(tt.want))
				return
			}

			for i, g := range got {
				if g.Text != tt.want[i].Text || g.Result != tt.want[i].Result {
					t.Errorf("criterion %d: got %+v, want %+v", i, g, tt.want[i])
				}
			}
		})
	}
}

func TestParseDebriefOutcome(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		want     []Criterion
		wantZero bool // true if we expect empty/nil result
	}{
		{
			name:     "empty section",
			section:  "",
			wantZero: true,
		},
		{
			name:    "single pass verdict",
			section: `- [x] First criterion — pass`,
			want: []Criterion{
				{Text: "- [x] First criterion — pass", Result: "met", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "mixed verdicts",
			section: `- [x] First — pass
- [x] Second — partial
- [ ] Third — unmet`,
			want: []Criterion{
				{Text: "- [x] First — pass", Result: "met", Evidence: ""},
				{Text: "- [x] Second — partial", Result: "partial", Evidence: ""},
				{Text: "- [ ] Third — unmet", Result: "unmet", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "verdict without checkbox",
			section: `- First — pass
- Second — met
- Third — failed`,
			want: []Criterion{
				{Text: "- First — pass", Result: "met", Evidence: ""},
				{Text: "- Second — met", Result: "met", Evidence: ""},
				{Text: "- Third — failed", Result: "unmet", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "checkbox without explicit verdict",
			section: `- [x] Checked criterion
- [ ] Unchecked criterion`,
			want: []Criterion{
				{Text: "- [x] Checked criterion", Result: "met", Evidence: ""},
				{Text: "- [ ] Unchecked criterion", Result: "unmet", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name:    "verdict takes precedence over checkbox",
			section: `- [x] Criterion — failed`,
			want: []Criterion{
				{Text: "- [x] Criterion — failed", Result: "unmet", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name:    "no verdict or checkbox",
			section: `- Some criterion text`,
			want: []Criterion{
				{Text: "- Some criterion text", Result: "unclear", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name:    "verdict with evidence",
			section: `- First — pass. This is why it passed.`,
			want: []Criterion{
				{Text: "- First — pass. This is why it passed.", Result: "met", Evidence: "This is why it passed."},
			},
			wantZero: false,
		},
		{
			name: "case insensitive verdict",
			section: `- [x] First — PASS
- [x] Second — Met
- [x] Third — PARTIAL`,
			want: []Criterion{
				{Text: "- [x] First — PASS", Result: "met", Evidence: ""},
				{Text: "- [x] Second — Met", Result: "met", Evidence: ""},
				{Text: "- [x] Third — PARTIAL", Result: "partial", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "various verdict keywords",
			section: `- [x] passed — passed
- [x] met — met
- [ ] not met — not met
- [ ] unmet — unmet
- [ ] not done — not done
- [ ] done — done`,
			want: []Criterion{
				{Text: "- [x] passed — passed", Result: "met", Evidence: ""},
				{Text: "- [x] met — met", Result: "met", Evidence: ""},
				{Text: "- [ ] not met — not met", Result: "unmet", Evidence: ""},
				{Text: "- [ ] unmet — unmet", Result: "unmet", Evidence: ""},
				{Text: "- [ ] not done — not done", Result: "unmet", Evidence: ""},
				{Text: "- [ ] done — done", Result: "met", Evidence: ""},
			},
			wantZero: false,
		},
		{
			name: "en/em/hyphen dashes",
			section: `- [x] First – pass
- [x] Second — pass
- [x] Third - pass`,
			want: []Criterion{
				{Text: "- [x] First – pass", Result: "met", Evidence: ""},
				{Text: "- [x] Second — pass", Result: "met", Evidence: ""},
				{Text: "- [x] Third - pass", Result: "met", Evidence: ""},
			},
			wantZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDebriefOutcome(tt.section)
			if tt.wantZero {
				if len(got) != 0 {
					t.Errorf("want empty result, got %d criteria", len(got))
				}
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("got %d criteria, want %d", len(got), len(tt.want))
				for i, g := range got {
					t.Logf("  [%d] %+v", i, g)
				}
				return
			}

			for i, g := range got {
				if g.Text != tt.want[i].Text || g.Result != tt.want[i].Result || g.Evidence != tt.want[i].Evidence {
					t.Errorf("criterion %d:\n  got %+v\n  want %+v", i, g, tt.want[i])
				}
			}
		})
	}
}

// TestParseDebriefOutcomeRealExample tests against the real BRIEF.md and DEBRIEF.md
// format from mission-20261013-03.
func TestParseDebriefOutcomeRealExample(t *testing.T) {
	// From DEBRIEF.md "Acceptance Criteria Outcome"
	section := `- [x] Reopen any decided directive proposal, then ratify it with edits or decline it — pass. Re-ratifying keeps the DIR-NNN; declining retires it. Server, UI and exporter all support this; verified on staging and on the live API.
- [x] Mark a stale (≥30 min idle) running triage run done or failed — pass. A note is required, the action is recorded in the audit log, and non-stale runs are refused. The agents' late finish call now gets a 409. Boundary and child-activity behaviour were verified on a database copy.
- [x] Staging on another port before going live — pass. 127.0.0.1:8788 ran against a database copy; the live service was untouched.
- [x] Live restart only after the operator's separate yes — pass. Yes given at 11:43 UTC; restart at 11:46 UTC; healthz ok; plist untouched.
- [ ] UI behaviour verified in a browser — partial. The operator excluded browser QA. The UI is verified by reading the code and by served-page checks only. The operator will try one reopen and one close.`

	got := ParseDebriefOutcome(section)

	if len(got) != 5 {
		t.Fatalf("got %d criteria, want 5", len(got))
	}

	if got[0].Result != "met" {
		t.Errorf("criterion 0: got result %q, want 'met'", got[0].Result)
	}

	if got[1].Result != "met" {
		t.Errorf("criterion 1: got result %q, want 'met'", got[1].Result)
	}

	if got[4].Result != "partial" {
		t.Errorf("criterion 4: got result %q, want 'partial'", got[4].Result)
	}

	// Verify evidence is extracted
	if got[0].Evidence == "" {
		t.Error("criterion 0: got empty evidence, want some text")
	}
}

// TestParseDebriefOutcomeCheckboxOnlyVariant tests briefing variants with only checkboxes.
func TestParseDebriefOutcomeCheckboxOnlyVariant(t *testing.T) {
	section := `- [x] Server endpoint functional
- [x] Database schema applied
- [ ] Browser test completed`

	got := ParseDebriefOutcome(section)

	if len(got) != 3 {
		t.Fatalf("got %d criteria, want 3", len(got))
	}

	tests := []struct {
		idx  int
		want string
	}{
		{0, "met"},
		{1, "met"},
		{2, "unmet"},
	}

	for _, tt := range tests {
		if got[tt.idx].Result != tt.want {
			t.Errorf("criterion %d: got result %q, want %q", tt.idx, got[tt.idx].Result, tt.want)
		}
	}
}

// TestParseBriefCriteriaRealExample tests against real BRIEF.md format.
func TestParseBriefCriteriaRealExample(t *testing.T) {
	section := `- Reopen button on decided directives; re-ratify amends DIR-NNN in place; decline retires to Retired Entries
- Operator can manually mark a running triage run done or failed when inactive for 30+ minutes (with note and decided-by)
- Flight-recorder row logged for each manual close
- Server refuses non-stale manual close requests`

	got := ParseBriefCriteria(section)

	if len(got) != 4 {
		t.Fatalf("got %d criteria, want 4", len(got))
	}

	for _, c := range got {
		if c.Result != "" {
			t.Errorf("brief criterion should have empty result, got %q", c.Result)
		}
	}
}
