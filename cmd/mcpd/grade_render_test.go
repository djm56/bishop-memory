package main

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestNewToolResultTextIsTextContentValue pins the content type the grade_claim
// wrapper type-asserts; if mcp-go changed it, the wrapper would silently fall
// back to raw JSON, the format OpenCode cannot read when it is large.
func TestNewToolResultTextIsTextContentValue(t *testing.T) {
	result := mcp.NewToolResultText("x")
	if _, ok := result.Content[0].(mcp.TextContent); !ok {
		t.Fatalf("NewToolResultText content is %T, want mcp.TextContent", result.Content[0])
	}
}

func TestRenderGradingPackets(t *testing.T) {
	long := strings.Repeat("word ", 180) // a 900-character debrief section
	body := `{"missions":[{"mission":{"id":"mission-20261001-01","title":"Ship it","harness":"kirsch","status":"complete","outcome":"done","opened_at":"2026-10-01 09:00:00","closed_at":"2026-10-01 11:00:00","duration_minutes":120},
		"signals":{"criteria_planned":3,"criteria_met":2,"criteria_partial":1,"criteria_unmet":0,"criteria_unclear":0,"steps":4,"steps_by_status":{"done":4},"injected_steps":2,"escalation_steps":0,"qa_steps":1,"blocked_events":0,"findings":1,"findings_by_status":{"proposed":1},"has_brief":true,"has_debrief":true},
		"brief":{"goal":"Ship it.","acceptance_criteria":"- one\n- two\n- three"},
		"debrief":{"Wrong Assumptions":"` + long + `","Acceptance Criteria Outcome":"- one — pass"},
		"steps":["1 @hicks done: built it","2a @hicks done: fix"],"findings":["#1 [proposed/brief-writing] @bishop: quote"],"notes":[]}]}`
	text, err := renderGradingPackets([]byte(body))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"=== MISSION mission-20261001-01 — Ship it", "criteria planned 3, met 2, partial 1", "injected steps 2",
		"DEBRIEF — Acceptance Criteria Outcome:\n- one — pass", "DEBRIEF — Wrong Assumptions:", "- 2a @hicks done: fix", "AGENT NOTES: (none)"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered packet lacks %q:\n%s", want, text)
		}
	}
	for i, line := range strings.Split(text, "\n") {
		if len(line) > 1500 {
			t.Errorf("line %d is %d characters; OpenCode's read tool cuts lines at 2,000", i+1, len(line))
		}
	}
	empty, _ := renderGradingPackets([]byte(`{"missions":[]}`))
	if !strings.HasPrefix(empty, "NO MISSIONS WAITING") {
		t.Errorf("empty claim rendered as %q", empty)
	}
}
