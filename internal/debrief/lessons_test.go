package debrief

import (
	"reflect"
	"testing"
)

func wa(text, why, correction string) Lesson {
	return Lesson{Kind: LessonWrongAssumption, Text: text, Why: why, Correction: correction}
}

func TestParseWrongAssumptions(t *testing.T) {
	cases := []struct {
		name        string
		section     string
		wantRows    []Lesson
		wantSkipped int
		wantOK      bool
	}{
		{"standard table", "| Assumption | Why It Was Wrong | Correction Applied |\n|---|---|---|\n| A one | why one | fix one |\n| A two | why two | fix two |\n",
			[]Lesson{wa("A one", "why one", "fix one"), wa("A two", "why two", "fix two")}, 0, true},
		{"lowercase header and escaped pipe", "| assumption | why | fix |\n|---|---|---|\n| a \\| b | w | c |\n",
			[]Lesson{wa("a | b", "w", "c")}, 0, true},
		{"none rows dropped and not counted", "| Assumption | Why | Fix |\n|---|---|---|\n| none | — | — |\n| None. | — | — |\n| — | — | — |\n|  | x | y |\n| Nonexistent | x | y |\n| real | w | c |\n",
			[]Lesson{wa("Nonexistent", "x", "y"), wa("real", "w", "c")}, 0, true},
		{"wrong cell count skipped and counted", "| Assumption | Why | Fix |\n|---|---|---|\n| a | b |\n| c | d | e | f |\n| g | h | i | j | k |\n| l | m | n |\n",
			[]Lesson{wa("l", "m", "n")}, 3, true},
		{"table ends at blank line", "| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\n\n| d | e | f |\n",
			[]Lesson{wa("a", "b", "c")}, 0, true},
		{"table ends at prose line", "| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\nSome prose.\n| d | e | f |\n",
			[]Lesson{wa("a", "b", "c")}, 0, true},
		{"aligned separator", "| Assumption | Why | Fix |\n|:---|:---:|---:|\n| a | b | c |\n",
			[]Lesson{wa("a", "b", "c")}, 0, true},
		{"crlf table", "| Assumption | Why | Fix |\r\n|:---|:---|:---|\r\n| a | b | c |\r\n",
			[]Lesson{wa("a", "b", "c")}, 0, true},
		{"header not first table", "intro prose\n| Other | x | y |\n|---|---|---|\n| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\n",
			[]Lesson{wa("a", "b", "c")}, 0, true},
		{"no assumption header", "| Agent | Mistake | Impact |\n|---|---|---|\n| a | b | c |\n", nil, 0, false},
		{"empty section", "", nil, 0, false},
		{"prose only", "No wrong assumptions this time.\n", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, skipped, ok := ParseWrongAssumptions(tc.section)
			if ok != tc.wantOK {
				t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			}
			if skipped != tc.wantSkipped {
				t.Errorf("%s: skipped = %d, want %d", tc.name, skipped, tc.wantSkipped)
			}
			if len(rows) != len(tc.wantRows) || (len(rows) > 0 && !reflect.DeepEqual(rows, tc.wantRows)) {
				t.Errorf("%s: rows = %#v, want %#v", tc.name, rows, tc.wantRows)
			}
		})
	}
}

func trig(text string) Lesson { return Lesson{Kind: LessonSimilarMission, Text: text} }
func step(text string) Lesson { return Lesson{Kind: LessonReuseStep, Text: text} }

func TestParseSimilarMission(t *testing.T) {
	cases := []struct {
		name     string
		section  string
		wantRows []Lesson
		wantOK   bool
	}{
		{"bullet trigger and indented steps", "- Trigger to reuse this report: any merge\n- Reuse checklist:\n  1. First step\n  2. Second step\n",
			[]Lesson{trig("any merge"), step("First step"), step("Second step")}, true},
		{"bold label colon inside", "**Trigger to reuse this report:** big refactor\n**Reuse checklist:**\n1. Do this\n",
			[]Lesson{trig("big refactor"), step("Do this")}, true},
		{"bold label colon outside", "- **Trigger to reuse this report**: live mirroring\n",
			[]Lesson{trig("live mirroring")}, true},
		{"short label", "**Trigger:** docs restructure\n", []Lesson{trig("docs restructure")}, true},
		{"lowercase label", "- trigger: lower\n", []Lesson{trig("lower")}, true},
		{"first trigger only", "- Trigger: one\n- Trigger: two\n", []Lesson{trig("one")}, true},
		{"empty trigger text ignored", "- Trigger:\n- Trigger: real\n", []Lesson{trig("real")}, true},
		{"steps only, trimmed with CR", "1. a\n  3. tail \r\n", []Lesson{step("a"), step("tail")}, true},
		{"multi-digit step and paren ignored", "10. ten\n1) not a step\n", []Lesson{step("ten")}, true},
		{"trigger with tab and CR", "-\tTrigger:\tx\r\n", []Lesson{trig("x")}, true},
		{"triggered by is a trigger", "Triggered by: release\n", []Lesson{trig("release")}, true},
		{"trigger after steps comes first", "1. s\n- Trigger: t\n", []Lesson{trig("t"), step("s")}, true},
		{"bold empty capture ignored", "**Trigger to reuse this report:**\n**Trigger:** later\n", []Lesson{trig("later")}, true},
		{"numbered trigger line is a step", "1. Trigger: x\n", []Lesson{step("Trigger: x")}, true},
		{"nothing readable", "Known open items at close.\n- something else\n", nil, false},
		{"empty section", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, ok := ParseSimilarMission(tc.section)
			if ok != tc.wantOK {
				t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			}
			if len(rows) != len(tc.wantRows) || (len(rows) > 0 && !reflect.DeepEqual(rows, tc.wantRows)) {
				t.Errorf("%s: rows = %#v, want %#v", tc.name, rows, tc.wantRows)
			}
		})
	}
}
