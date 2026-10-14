// Package criteria parses acceptance criteria from mission briefs and debriefs.
//
// This package provides the shared parsing logic for:
//   - Extracting criterion lines from BRIEF.md "Acceptance Criteria" sections
//   - Extracting criterion outcomes from DEBRIEF.md "Acceptance Criteria Outcome" sections
//   - Matching debrief outcomes to their verdict results (met, partial, unmet, unclear)
//
// Both the importer (internal/importer/criteria.go) and the grader (internal/api/grades.go)
// use the parsing functions and verdict-matching logic defined in this package.
package criteria

import (
	"regexp"
	"strings"
)

// bulletLine matches lines starting with - or * (bullet points).
// Used to find criterion lines in both brief and debrief sections.
var bulletLine = regexp.MustCompile(`(?m)^\s*[-*]\s+\S.*$`)

// criterionVerdict finds the result keyword after a dash in a debrief line.
// Debrief outcomes write a verdict (pass, met, partial, etc.) after a dash.
// The word takes precedence over a checkbox; a line with neither word nor checkbox is unclear.
var criterionVerdict = regexp.MustCompile(`(?i)[—–-]\s*\**\s*(not met|not done|unmet|partially met|partial|pass(?:ed)?|met|fail(?:ed)?|deferred|done)\b`)

// criterionBox matches checkboxes [ ] or [x] / [X] on a line.
// Used as a fallback when no explicit verdict word is present.
var criterionBox = regexp.MustCompile(`^\s*[-*]\s*\[([ xX])\]`)

// Criterion is one parsed acceptance criterion line with its optional result.
type Criterion struct {
	// Text is the criterion text (the bullet line from brief or debrief).
	Text string
	// Result is the outcome: "met", "partial", "unmet", "unclear", or "" if not yet determined.
	// Empty string indicates the criterion has not been graded (from brief only, no debrief yet).
	Result string
	// Evidence is optional text after the verdict (e.g., notes on why it passed or failed).
	Evidence string
}

// ParseBriefCriteria extracts criterion lines from a brief's "Acceptance Criteria" section.
// Lines are parsed from bullet points; the result is always empty (not yet graded).
// Returns a list of criteria ordered as they appear in the section.
// An empty or missing section returns an empty list (not an error).
func ParseBriefCriteria(section string) []Criterion {
	if section == "" {
		return nil
	}

	var criteria []Criterion
	for _, line := range bulletLine.FindAllString(section, -1) {
		text := strings.TrimSpace(line)
		if text != "" {
			criteria = append(criteria, Criterion{
				Text:     text,
				Result:   "",
				Evidence: "",
			})
		}
	}
	return criteria
}

// ParseDebriefOutcome extracts criteria and their verdicts from a debrief's
// "Acceptance Criteria Outcome" section. Each line is parsed for a verdict keyword
// and optional evidence text. Lines are matched to their verdicts as follows:
//
// 1. If a line contains a verdict keyword (pass, met, partial, etc.), use that.
// 2. Otherwise, if a line contains a checkbox, use checked=met, unchecked=unmet.
// 3. Otherwise, mark the line as unclear.
//
// Evidence is the text after the verdict keyword (if present).
// Returns a list of criteria with results, ordered as they appear in the section.
// An empty or missing section returns an empty list (not an error).
func ParseDebriefOutcome(section string) []Criterion {
	if section == "" {
		return nil
	}

	var criteria []Criterion
	for _, line := range bulletLine.FindAllString(section, -1) {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}

		result, evidence := parseVerdict(line)
		criteria = append(criteria, Criterion{
			Text:     text,
			Result:   result,
			Evidence: evidence,
		})
	}
	return criteria
}

// parseVerdict extracts the result and optional evidence from a single debrief line.
// Returns (result, evidence) where result is one of "met", "partial", "unmet", "unclear"
// and evidence is the text after the verdict keyword.
func parseVerdict(line string) (result, evidence string) {
	// Check for an explicit verdict word.
	if m := criterionVerdict.FindStringSubmatchIndex(line); m != nil {
		// m[2] and m[3] are the start and end of capture group 1 (the verdict word).
		verdict := strings.ToLower(line[m[2]:m[3]])
		result = matchVerdictToResult(verdict)
		// Extract evidence: everything after the verdict match (m[1] is end of full match).
		if m[1] < len(line) {
			evidence = strings.TrimLeft(line[m[1]:], ". ")
		}
		return result, evidence
	}

	// Fallback to checkbox.
	if box := criterionBox.FindStringSubmatch(line); box != nil {
		if box[1] == " " {
			return "unmet", ""
		}
		return "met", ""
	}

	// No verdict found.
	return "unclear", ""
}

// matchVerdictToResult maps a verdict keyword to a result value.
func matchVerdictToResult(verdict string) string {
	switch verdict {
	case "pass", "passed", "met", "done":
		return "met"
	case "partial", "partially met":
		return "partial"
	case "not met", "not done", "unmet", "fail", "failed", "deferred":
		return "unmet"
	default:
		return "unclear"
	}
}
